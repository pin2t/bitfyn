// Command gentx generates and signs a transaction spending exactly the given
// wallet coins to one address, with no change, and prints it as hex. It
// sends nothing: the hex is to be broadcast by hand, such as with bitcoin-cli
// sendrawtransaction or a block explorer. It repairs a payment stuck because
// one of its coins was spent by another wallet on the same seed, paying
// again from the coins still unspent. The wallet database is only read. The
// coins must be outputs of confirmed wallet transactions.
package main

import "bytes"
import "encoding/hex"
import "flag"
import "fmt"
import "os"
import "path/filepath"
import "strings"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/txscript"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/storage"
import "bitfyn/internal/wallet"

func main() {
	var home, _ = os.UserHomeDir()
	var dataDir = flag.String("datadir", filepath.Join(home, ".bitfyn"), "directory of the wallet database")
	var network = flag.String("net", "mainnet", "bitcoin network: mainnet, testnet, regtest or simnet")
	var dbPass = flag.String("dbpass", "", "passphrase of the encrypted database")
	var coins = flag.String("coins", "", "comma separated coins to spend, as txid:index")
	var to = flag.String("to", "", "address to pay")
	var feeRate = flag.Int64("feerate", 2, "fee rate in sat/vB")
	flag.Parse()
	if *coins == "" || *to == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*dataDir, *network, *dbPass, strings.Split(*coins, ","), *to, *feeRate); err != nil {
		fmt.Fprintln(os.Stderr, "gentx:", err)
		os.Exit(1)
	}
}

// run signs the spend of the coins to the address and prints it.
func run(dataDir, network, dbPass string, outpoints []string, to string, feeRate int64) error {
	var net, err = wallet.ParamsForNetwork(network)
	if err != nil { return err }
	var store, serr = storage.Open(filepath.Join(dataDir, "bitfyn.db"), dbPass)
	if serr != nil { return serr }
	defer store.Close()
	var meta, merr = store.Meta()
	if merr != nil { return merr }
	if meta.Network != network {
		return fmt.Errorf("wallet database is for network %q, not %q", meta.Network, network)
	}
	var w, werr = wallet.New(meta.Mnemonic, "", net)
	if werr != nil { return werr }
	var inputs, cerr = findCoins(store, net, outpoints)
	if cerr != nil { return cerr }
	var dest, derr = w.AddressScript(to)
	if derr != nil { return derr }
	var amount = wallet.MaxAmount(inputs, dest, feeRate)
	if amount == 0 {
		return fmt.Errorf("the coins cannot pay more than dust at %d sat/vB", feeRate)
	}
	var sum = int64(0)
	for _, c := range inputs {
		sum += c.Value
	}
	var tx, txerr = w.SignSpend(wallet.Spend{Inputs: inputs, Amount: amount, Fee: sum - amount}, dest, nil)
	if txerr != nil { return txerr }
	if err := verify(tx, inputs); err != nil { return err }
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil { return err }
	var vsize = (int64(tx.SerializeSizeStripped())*3 + int64(tx.SerializeSize()) + 3) / 4
	fmt.Printf("txid    %s\n", tx.TxHash())
	for _, c := range inputs {
		fmt.Printf("spends  %s  %d sats from %s\n", c.OutPoint, c.Value, c.Address)
	}
	fmt.Printf("pays    %d sats to %s\n", amount, to)
	fmt.Printf("fee     %d sats, %d vB, %.2f sat/vB\n", sum-amount, vsize, float64(sum-amount)/float64(vsize))
	fmt.Printf("hex     %s\n", hex.EncodeToString(buf.Bytes()))
	return nil
}

// findCoins looks the outpoints up in the confirmed wallet transactions,
// with the derivation path of the address each pays, and refuses one that a
// stored transaction spends.
func findCoins(store *storage.Store, net *chaincfg.Params, outpoints []string) ([]wallet.Coin, error) {
	var stored, err = store.Transactions()
	if err != nil { return nil, err }
	var txs = make(map[string]*wire.MsgTx, len(stored))
	var spent = make(map[wire.OutPoint]string)
	for _, t := range stored {
		var tx wire.MsgTx
		if err := tx.Deserialize(bytes.NewReader(t.Raw)); err != nil { return nil, err }
		txs[t.Txid.String()] = &tx
		for _, in := range tx.TxIn {
			spent[in.PreviousOutPoint] = t.Txid.String()
		}
	}
	var paths, perr = addressPaths(store)
	if perr != nil { return nil, perr }
	var coins []wallet.Coin
	for _, s := range outpoints {
		var op, oerr = wire.NewOutPointFromString(strings.TrimSpace(s))
		if oerr != nil { return nil, fmt.Errorf("coin %q: %w", s, oerr) }
		var tx, ok = txs[op.Hash.String()]
		if !ok || int(op.Index) >= len(tx.TxOut) {
			return nil, fmt.Errorf("coin %s is not an output of a confirmed wallet transaction", op)
		}
		if by, ok := spent[*op]; ok {
			return nil, fmt.Errorf("coin %s is already spent by %s", op, by)
		}
		var out = tx.TxOut[op.Index]
		var _, addrs, _, _ = txscript.ExtractPkScriptAddrs(out.PkScript, net)
		if len(addrs) != 1 {
			return nil, fmt.Errorf("coin %s has no single address", op)
		}
		var address = addrs[0].EncodeAddress()
		var path, mine = paths[address]
		if !mine {
			return nil, fmt.Errorf("coin %s pays %s, not a wallet address", op, address)
		}
		coins = append(coins, wallet.Coin{OutPoint: *op, Value: out.Value, PkScript: out.PkScript, Address: address, Path: path, Confirmed: true})
	}
	return coins, nil
}

// addressPaths maps every stored receive and change address to its
// derivation path.
func addressPaths(store *storage.Store) (map[string]string, error) {
	var receive, err = store.Addresses()
	if err != nil { return nil, err }
	var change, cerr = store.ChangeAddresses()
	if cerr != nil { return nil, cerr }
	var paths = make(map[string]string, len(receive)+len(change))
	for _, a := range append(receive, change...) {
		paths[a.Address] = a.Path
	}
	return paths, nil
}

// verify runs every input script of the signed transaction, as a node
// would before accepting it.
func verify(tx *wire.MsgTx, coins []wallet.Coin) error {
	var fetcher = txscript.NewMultiPrevOutFetcher(nil)
	var values = make(map[wire.OutPoint]wallet.Coin, len(coins))
	for _, c := range coins {
		fetcher.AddPrevOut(c.OutPoint, wire.NewTxOut(c.Value, c.PkScript))
		values[c.OutPoint] = c
	}
	var hashes = txscript.NewTxSigHashes(tx, fetcher)
	for i, in := range tx.TxIn {
		var c = values[in.PreviousOutPoint]
		var vm, err = txscript.NewEngine(c.PkScript, tx, i, txscript.StandardVerifyFlags, nil, hashes, c.Value, fetcher)
		if err == nil { err = vm.Execute() }
		if err != nil { return fmt.Errorf("input %d does not verify: %w", i, err) }
	}
	return nil
}
