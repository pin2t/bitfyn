// Command bitfyn is an SPV Bitcoin wallet with a Fyne GUI.
package main

import "errors"
import "flag"
import "fmt"
import "log"
import "os"
import "path/filepath"
import "time"
import "github.com/skip2/go-qrcode"
import "bitfyn/internal/gui"
import "bitfyn/internal/storage"
import "bitfyn/internal/wallet"

func main() {
	var dataDir = flag.String("datadir", defaultDataDir(), "directory for the wallet database")
	var network = flag.String("net", "mainnet", "bitcoin network: mainnet, testnet, regtest or simnet")
	var dbPass  = flag.String("dbpass", "", "passphrase for the encrypted SQLCipher database (empty = unencrypted)")
	var check   = flag.Bool("check", false, "initialise the wallet and print its address without opening the GUI")
	var peer    = flag.String("peer", "", "P2P peer to sync from first (host:port); default: stored peers and DNS seeds for the network")
	flag.Parse()
	var logFile, err = setupLogging(*dataDir)
	if err != nil {
		log.Printf("log file: %v", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}
	if *check {
		if err := runCheck(*dataDir, *network, *dbPass); err != nil {
			log.Printf("check failed: %v", err)
			fmt.Fprintf(os.Stderr, "check failed: %v\n", err)
			os.Exit(1)
		}
		return
	}
	gui.Run(gui.Options{DataDir: *dataDir, Network: *network, DBPass: *dbPass, Peer: *peer})
}

// setupLogging sends the log to the console when bitfyn runs from a
// terminal, and to bitfyn.log in the data directory otherwise, for example
// when it is started from a desktop launcher. The returned file, if any, must
// be closed on exit.
func setupLogging(dataDir string) (*os.File, error) {
	if isTerminal(os.Stderr) {
		return nil, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	var f, err = os.OpenFile(filepath.Join(dataDir, "bitfyn.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	log.SetOutput(f)
	log.Printf("bitfyn started, pid %d", os.Getpid())
	return f, nil
}

// isTerminal reports whether the file is an interactive terminal.
func isTerminal(f *os.File) bool {
	var info, err = f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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

