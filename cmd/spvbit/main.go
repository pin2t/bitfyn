// Command spvbit is an SPV Bitcoin wallet with a Fyne GUI.
package main

import "errors"
import "flag"
import "fmt"
import "log"
import "os"
import "path/filepath"
import "time"
import "github.com/skip2/go-qrcode"
import "spvbit/internal/gui"
import "spvbit/internal/storage"
import "spvbit/internal/wallet"

func defaultDataDir() string {
	var home, err = os.UserHomeDir()
	if err != nil {
		return ".spvbit"
	}
	return filepath.Join(home, ".spvbit")
}

func main() {
	var (
		dataDir = flag.String("datadir", defaultDataDir(), "directory for the wallet database")
		network = flag.String("net", "mainnet", "bitcoin network: mainnet, testnet, regtest or simnet")
		dbPass  = flag.String("dbpass", "", "passphrase for the encrypted SQLCipher database (empty = unencrypted)")
		check   = flag.Bool("check", false, "initialise the wallet and print its address without opening the GUI")
	)
	flag.Parse()
	if *check {
		if err := runCheck(*dataDir, *network, *dbPass); err != nil {
			log.Fatalf("check failed: %v", err)
		}
		return
	}
	gui.Run(gui.Options{
		DataDir: *dataDir,
		Network: *network,
		DBPass:  *dbPass,
	})
}

// runCheck exercises the full non-GUI pipeline: database, HD wallet, address
// derivation and QR rendering. Useful for CI and quick smoke tests.
func runCheck(dataDir, network, dbPass string) error {
	var net, err = wallet.ParamsForNetwork(network)
	if err != nil {
		return err
	}
	store, err := storage.Open(filepath.Join(dataDir, "spvbit.db"), dbPass)
	if err != nil {
		return err
	}
	defer store.Close()
	meta, err := store.Meta()
	var w *wallet.Wallet
	switch {
	case errors.Is(err, storage.ErrNoWallet):
		var mnemonic, err = wallet.NewMnemonic(128)
		if err != nil {
			return err
		}
		w, err = wallet.New(mnemonic, "", net)
		if err != nil {
			return err
		}
		xpub, err := w.AccountXPub()
		if err != nil {
			return err
		}
		if err := store.SaveMeta(mnemonic, xpub, network, time.Now().Unix()); err != nil {
			return err
		}
		meta, err = store.Meta()
		if err != nil {
			return err
		}
		fmt.Println("created new wallet")
	case err != nil:
		return err
	default:
		if meta.Network != network {
			return fmt.Errorf("wallet database is for network %q, requested %q", meta.Network, network)
		}
		w, err = wallet.New(meta.Mnemonic, "", net)
		if err != nil {
			return err
		}
	}
	addr, path, pub, err := w.DeriveAddress(meta.NextIndex)
	if err != nil {
		return err
	}
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
