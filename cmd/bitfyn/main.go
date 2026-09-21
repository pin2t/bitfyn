// Command bitfyn is an SPV Bitcoin wallet with a Fyne GUI.
package main

import "errors"
import "flag"
import "fmt"
import "log"
import "math/rand/v2"
import "net"
import "os"
import "path/filepath"
import "strconv"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/skip2/go-qrcode"
import "bitfyn/internal/gui"
import "bitfyn/internal/p2p"
import "bitfyn/internal/spv"
import "bitfyn/internal/storage"
import "bitfyn/internal/wallet"

func main() {
	var dataDir = flag.String("datadir", defaultDataDir(), "directory for the wallet database")
	var network = flag.String("net", "mainnet", "bitcoin network: mainnet, testnet, regtest or simnet")
	var dbPass  = flag.String("dbpass", "", "passphrase for the encrypted SQLCipher database (empty = unencrypted)")
	var check   = flag.Bool("check", false, "initialise the wallet and print its address without opening the GUI")
	var sync    = flag.Bool("sync", false, "sync block headers and BIP158 filters from a P2P peer, then match wallet addresses")
	var peer    = flag.String("peer", "", "P2P peer to sync from (host:port); default: a DNS seed for the network")
	flag.Parse()
	if *check {
		if err := runCheck(*dataDir, *network, *dbPass); err != nil {
			log.Fatalf("check failed: %v", err)
		}
		return
	}
	if *sync {
		if err := runSync(*dataDir, *network, *dbPass, *peer); err != nil {
			log.Fatalf("sync failed: %v", err)
		}
		return
	}
	gui.Run(gui.Options{DataDir: *dataDir, Network: *network, DBPass: *dbPass})
}

func defaultDataDir() string {
	var home, err = os.UserHomeDir()
	if err != nil { return ".bitfyn" }
	return filepath.Join(home, ".bitfyn")
}

// openWallet opens the database, creating the wallet on first run, and
// returns the store with the loaded wallet.
func openWallet(dataDir, network, dbPass string) (*storage.Store, *wallet.Wallet, error) {
	var net, err = wallet.ParamsForNetwork(network)
	if err != nil { return nil, nil, err }
	store, err := storage.Open(filepath.Join(dataDir, "bitfyn.db"), dbPass)
	if err != nil { return nil, nil, err }
	var fail = func(err error) (*storage.Store, *wallet.Wallet, error) {
		_ = store.Close()
		return nil, nil, err
	}
	meta, err := store.Meta()
	var w *wallet.Wallet
	switch {
	case errors.Is(err, storage.ErrNoWallet):
		var mnemonic, err = wallet.NewMnemonic(128)
		if err != nil { return fail(err) }
		w, err = wallet.New(mnemonic, "", net)
		if err != nil { return fail(err) }
		xpub, err := w.AccountXPub()
		if err != nil { return fail(err) }
		if err := store.SaveMeta(mnemonic, xpub, network, time.Now().Unix()); err != nil {
			return fail(err)
		}
	case err != nil:
		return fail(err)
	default:
		if meta.Network != network {
			return fail(fmt.Errorf("wallet database is for network %q, requested %q", meta.Network, network))
		}
		w, err = wallet.New(meta.Mnemonic, "", net)
		if err != nil { return fail(err) }
	}
	return store, w, nil
}

// runCheck exercises the full non-GUI pipeline: database, HD wallet, address
// derivation and QR rendering. Useful for CI and quick smoke tests.
func runCheck(dataDir, network, dbPass string) error {
	var store, w, err = openWallet(dataDir, network, dbPass)
	if err != nil { return err }
	defer store.Close()
	meta, err := store.Meta()
	if err != nil { return err }
	addr, path, pub, err := w.DeriveAddress(meta.NextIndex)
	if err != nil { return err }
	if err := store.AddAddress(meta.NextIndex, path, addr, pub); err != nil {
		return err
	}
	var qrPath = filepath.Join(dataDir, "address-qr.png")
	if err := qrcode.WriteFile(addr, qrcode.Medium, 256, qrPath); err != nil {
		return err
	}
	fmt.Printf("network:      %s\n", meta.Network)
	fmt.Printf("account xpub: %s\n", meta.XPub)
	fmt.Printf("next index:   %d\n", meta.NextIndex)
	fmt.Printf("path:         %s\n", path)
	fmt.Printf("address:      %s\n", addr)
	fmt.Printf("qr:           %s\n", qrPath)
	fmt.Printf("encrypted:    %v\n", dbPass != "")
	return nil
}

// runSync performs the SPV sync: block headers, BIP158 basic filters and
// wallet address matching. Peers are tried one by one; a disconnected or
// failing peer is replaced by the next candidate.
func runSync(dataDir, network, dbPass, peerAddr string) error {
	var net, err = wallet.ParamsForNetwork(network)
	if err != nil { return err }
	var store, w, err2 = openWallet(dataDir, network, dbPass)
	if err2 != nil { return err2 }
	defer store.Close()
	count, err := store.CountAddresses()
	if err != nil { return err }
	if count == 0 {
		var meta, err = store.Meta()
		if err != nil { return err }
		var addr, path, pub, derr = w.DeriveAddress(meta.NextIndex)
		if derr != nil { return derr }
		if err := store.AddAddress(meta.NextIndex, path, addr, pub); err != nil {
			return err
		}
	}
	syncer, err := spv.NewSyncer(net, store, syncProgress)
	if err != nil { return err }
	var candidates, err3 = syncPeers(net, store, peerAddr)
	if err3 != nil { return err3 }
	var tip int32
	var filters int32
	var lastErr error
	for _, addr := range candidates {
		tip, filters, err = syncFromPeer(net, store, syncer, addr)
		if err == nil { break }
		lastErr = err
		log.Printf("sync via %s failed: %v", addr, err)
	}
	if lastErr != nil {
		return fmt.Errorf("all %d peers failed, last error: %w", len(candidates), lastErr)
	}
	matches, err := store.Matches()
	if err != nil { return err }
	var blocks = make(map[int32]bool)
	for _, m := range matches {
		blocks[m.Height] = true
	}
	fmt.Printf("tip:     height %d\n", tip)
	fmt.Printf("filters: %d downloaded\n", filters)
	fmt.Printf("matches: %d script hits in %d blocks\n", len(matches), len(blocks))
	return nil
}

// syncPeers builds the ordered list of peers to try: the explicit address
// first when given, then stored and freshly discovered peers in random order.
func syncPeers(params *chaincfg.Params, store *storage.Store, explicit string) ([]string, error) {
	var list []string
	var seen = make(map[string]bool)
	var add = func(addr string) {
		if addr == "" || seen[addr] { return }
		seen[addr] = true
		list = append(list, addr)
	}
	var start = 0
	if explicit != "" {
		if _, _, err := net.SplitHostPort(explicit); err != nil {
			return nil, fmt.Errorf("peer address %q must include a port", explicit)
		}
		add(explicit)
		start = 1
	}
	var stored, err = store.Peers()
	if err != nil { return nil, err }
	for _, p := range stored {
		add(net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
	}
	for _, seed := range p2p.Seeds(params) {
		add(seed.String())
	}
	rand.Shuffle(len(list)-start, func(i, j int) {
		list[start+i], list[start+j] = list[start+j], list[start+i]
	})
	if len(list) == 0 {
		return nil, fmt.Errorf("no peer addresses for network %s (pass -peer or run a local node)", params.Name)
	}
	return list, nil
}

// syncFromPeer dials one peer and runs the header and filter sync on it,
// storing the peer address for future runs.
func syncFromPeer(params *chaincfg.Params, store *storage.Store, syncer *spv.Syncer, addr string) (int32, int32, error) {
	log.Printf("syncing from %s", addr)
	var conn, err = p2p.Dial(params, addr, syncer.Listeners())
	if err != nil { return 0, 0, err }
	defer func() {
		conn.Disconnect()
		conn.WaitForDisconnect()
	}()
	var host, port, perr = splitHostPort(addr)
	if perr == nil {
		if serr := store.SavePeer(host, port); serr != nil {
			log.Printf("store peer %s: %v", addr, serr)
		}
	}
	var tip, herr = syncer.SyncHeaders(conn)
	if herr != nil { return 0, 0, fmt.Errorf("headers: %w", herr) }
	var filters, ferr = syncer.SyncFilters(conn)
	if ferr != nil { return 0, 0, fmt.Errorf("filters: %w", ferr) }
	return tip, filters, nil
}

// splitHostPort splits a host:port address into its parts.
func splitHostPort(addr string) (string, uint16, error) {
	var host, port, err = net.SplitHostPort(addr)
	if err != nil { return "", 0, err }
	var num, err2 = strconv.Atoi(port)
	if err2 != nil { return "", 0, err2 }
	return host, uint16(num), nil
}

// syncProgress logs the sync stage and the height reached so far.
func syncProgress(stage string, height int32) {
	log.Printf("sync: %s at height %d", stage, height)
}
