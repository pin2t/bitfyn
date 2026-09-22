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
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"
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
// wallet address matching. Headers are synced from one peer, the first
// filter header is then majority proven from several peers, and the filter
// chain is finally downloaded from one peer. A disconnected or failing peer
// is replaced by the next candidate.
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
	if err := spv.Init(net, store, syncProgress); err != nil {
		return err
	}
	var candidates, err3 = syncPeers(net, store, peerAddr)
	if err3 != nil { return err3 }
	var tip, herr = syncStageFromPeers(net, store, &candidates, stageHeaders)
	if herr != nil { return herr }
	spv.RefreshFilterStart()
	if spv.NeedsAnchor() {
		var anchor, ok, aerr = proveFilterAnchor(net, store, candidates)
		if aerr != nil {
			return fmt.Errorf("prove filter header anchor: %w", aerr)
		}
		if ok {
			spv.SetFilterAnchor(anchor)
			log.Printf("filter header anchor proven by peer majority: %s", anchor)
		} else {
			log.Printf("no peer answered the filter header anchor request; the first filter peer will be trusted")
		}
	}
	var filters, ferr = syncStageFromPeers(net, store, &candidates, stageFilters)
	if ferr != nil { return ferr }
	matches, err := store.Matches()
	if err != nil { return err }
	var blocks = make(map[int32]bool)
	for _, m := range matches {
		blocks[m.Height] = true
	}
	fmt.Printf("tip:     height %d\n", tip)
	fmt.Printf("filters: %d downloaded (from height %d)\n", filters, spv.FilterStart())
	fmt.Printf("matches: %d script hits in %d blocks\n", len(matches), len(blocks))
	return nil
}

// syncPeers builds the ordered list of peers to try: the explicit address
// first when given, then stored and freshly discovered peers in random order.
// Stored peers that are known not to serve compact filters are skipped.
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
		if p.Services != 0 && p.Services&uint64(wire.SFNodeCF) == 0 { continue }
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

// syncStage selects which part of the sync one peer connection runs.
type syncStage int

const stageHeaders syncStage = 0
const stageFilters syncStage = 1

// anchorPeerLimit is how many peers are asked for the first filter header
// when majority proving the filter header chain anchor.
const anchorPeerLimit = 10

// syncStageFromPeers runs one sync stage against the candidate peers in
// order, failing over to the next peer on error. Newly learned peers are
// appended to the candidate list between attempts. It returns the number
// reported by the stage: the header tip for stageHeaders, the downloaded
// filter count for stageFilters.
func syncStageFromPeers(params *chaincfg.Params, store *storage.Store, candidates *[]string, stage syncStage) (int32, error) {
	var lastErr error
	for i := 0; i < len(*candidates); i++ {
		var addr = (*candidates)[i]
		var n, err = syncFromPeer(params, store, addr, stage)
		if err == nil { return n, nil }
		lastErr = err
		log.Printf("sync via %s failed: %v", addr, err)
		addLearnedPeers(params, store, candidates)
	}
	return 0, fmt.Errorf("all %d peers failed, last error: %w", len(*candidates), lastErr)
}

// proveFilterAnchor asks up to anchorPeerLimit peers for the first filter
// header and returns the value reported by a strict majority. ok is false
// when no peer responded; the caller then falls back to trusting the first
// filter peer. Peers disagreeing without a majority are an error.
func proveFilterAnchor(params *chaincfg.Params, store *storage.Store, candidates []string) (chainhash.Hash, bool, error) {
	var votes []chainhash.Hash
	for _, addr := range candidates {
		if len(votes) >= anchorPeerLimit { break }
		var host, port, perr = splitHostPort(addr)
		if perr != nil { continue }
		var conn, _, derr = p2p.Dial(params, addr, spv.Listeners())
		if derr != nil {
			_ = store.RecordPeerResult(host, port, false, 0)
			continue
		}
		var flags = uint64(conn.Services())
		if serr := store.UpdatePeerServices(host, port, flags); serr != nil {
			log.Printf("store peer %s: %v", addr, serr)
		}
		if flags&uint64(wire.SFNodeCF) == 0 {
			conn.Disconnect()
			conn.WaitForDisconnect()
			continue
		}
		var anchor, aerr = spv.RequestFilterAnchor(conn)
		_ = store.RecordPeerResult(host, port, aerr == nil, 0)
		conn.Disconnect()
		conn.WaitForDisconnect()
		if aerr != nil {
			log.Printf("filter header anchor from %s failed: %v", addr, aerr)
			continue
		}
		votes = append(votes, anchor)
	}
	if len(votes) == 0 {
		return chainhash.Hash{}, false, nil
	}
	var anchor, err = spv.MajorityAnchor(votes)
	if err != nil {
		return chainhash.Hash{}, false, err
	}
	return anchor, true, nil
}

// syncFromPeer dials one peer and runs one sync stage on it. The advertised
// services and the handshake latency are stored on connect, every request
// outcome and latency is recorded, and a peer without the compact filters
// service is rejected before any request is made.
func syncFromPeer(params *chaincfg.Params, store *storage.Store, addr string, stage syncStage) (int32, error) {
	log.Printf("syncing from %s", addr)
	var host, port, perr = splitHostPort(addr)
	if perr != nil { return 0, perr }
	var conn, handshake, err = p2p.Dial(params, addr, spv.Listeners())
	if err != nil {
		if rerr := store.RecordPeerResult(host, port, false, 0); rerr != nil {
			log.Printf("record peer %s: %v", addr, rerr)
		}
		return 0, err
	}
	defer func() {
		conn.Disconnect()
		conn.WaitForDisconnect()
	}()
	var flags = uint64(conn.Services())
	if serr := store.UpsertPeer(storage.Peer{Host: host, Port: port, Services: flags, LatencyMs: handshake.Milliseconds()}); serr != nil {
		log.Printf("store peer %s: %v", addr, serr)
	}
	if flags&uint64(wire.SFNodeCF) == 0 {
		return 0, fmt.Errorf("peer does not advertise compact filters")
	}
	spv.SetStats(func(ok bool, latency time.Duration) {
		if rerr := store.RecordPeerResult(host, port, ok, latency.Milliseconds()); rerr != nil {
			log.Printf("record peer %s: %v", addr, rerr)
		}
	})
	if aerr := spv.RequestAddresses(conn); aerr != nil {
		log.Printf("peer %s: %v", addr, aerr)
	}
	if stage == stageHeaders {
		var tip, herr = spv.SyncHeaders(conn)
		if herr != nil { return 0, fmt.Errorf("headers: %w", herr) }
		return tip, nil
	}
	var filters, ferr = spv.SyncFilters(conn)
	if ferr != nil { return 0, fmt.Errorf("filters: %w", ferr) }
	return filters, nil
}

// addLearnedPeers appends peers discovered from connected peers since the
// last attempt, so they can be used within the current run too.
func addLearnedPeers(params *chaincfg.Params, store *storage.Store, candidates *[]string) {
	var fresh, err = store.Peers()
	if err != nil { return }
	var seen = make(map[string]bool)
	for _, addr := range *candidates {
		seen[addr] = true
	}
	for _, p := range fresh {
		if p.Services != 0 && p.Services&uint64(wire.SFNodeCF) == 0 { continue }
		var addr = net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
		if seen[addr] { continue }
		seen[addr] = true
		*candidates = append(*candidates, addr)
	}
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
