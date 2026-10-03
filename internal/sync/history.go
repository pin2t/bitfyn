package sync

import "sort"
import "github.com/btcsuite/btcd/btcutil"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/txscript"
import "github.com/btcsuite/btcd/wire"

// WalletTx is a wallet transaction as the history shows it: when it was
// confirmed or first seen, what it did to the wallet balance, the fee the
// wallet paid, set when every input spends a wallet coin, and the addresses
// it spends from and pays to.
type WalletTx struct {
	Txid      chainhash.Hash
	Time      int64
	Confirmed bool
	Net       int64
	Fee       int64
	Inputs    []TxAddress
	Outputs   []TxAddress
}

// TxAddress is an address a transaction spends from or pays to, and whether
// it is a wallet address. Address is empty when it cannot be told: an input
// whose previous output the wallet does not hold, spending a script that
// leaves no trace of its address. A coinbase input reads "coinbase".
type TxAddress struct {
	Address string
	Mine    bool
}

// coinbaseInput names the input of a coinbase transaction.
const coinbaseInput = "coinbase"

// history is the wallet transactions, newest first, rebuilt with the wallet
// state. walletMu guards it.
var history []WalletTx

// History returns the wallet transactions, confirmed and unconfirmed, the
// newest first.
func History() []WalletTx {
	walletMu.Lock()
	defer walletMu.Unlock()
	return append([]WalletTx(nil), history...)
}

// buildHistory describes the wallet transactions, each with its time, from
// the confirmed ones in block order to the unconfirmed ones, and returns
// them newest first. walletMu must be held.
func buildHistory(txs []*wire.MsgTx, times []int64, confirmed int) []WalletTx {
	var byID = make(map[chainhash.Hash]*wire.MsgTx, len(txs))
	for _, tx := range txs {
		byID[tx.TxHash()] = tx
	}
	var out = make([]WalletTx, len(txs))
	for n, tx := range txs {
		var entry = WalletTx{Txid: tx.TxHash(), Time: times[n], Confirmed: n < confirmed}
		var funded = true
		var fee = int64(0)
		for _, in := range tx.TxIn {
			var addr, value = inputAddress(in, byID)
			entry.Inputs = append(entry.Inputs, addr)
			funded = funded && addr.Mine
			fee += value
			if addr.Mine { entry.Net -= value }
		}
		for _, o := range tx.TxOut {
			var addr = outputAddress(o.PkScript)
			entry.Outputs = append(entry.Outputs, addr)
			fee -= o.Value
			if addr.Mine { entry.Net += o.Value }
		}
		if funded { entry.Fee = fee }
		out[n] = entry
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confirmed != out[j].Confirmed { return !out[i].Confirmed }
		return out[i].Time > out[j].Time
	})
	return out
}

// outputAddress is the address an output script pays, telling the wallet's
// own by its scripts. A script with no address, such as OP_RETURN data,
// gets none.
func outputAddress(script []byte) TxAddress {
	if idx, ok := scriptIndex[string(script)]; ok {
		return TxAddress{Address: scripts[idx].address, Mine: true}
	}
	return TxAddress{Address: scriptAddress(script)}
}

// inputAddress is the address an input spends from, with the value it
// spends when the wallet holds the previous transaction. Otherwise the
// address is read from the signature script and witness, where the common
// script types leave the key or script they pay to. Relayed unconfirmed
// transactions come without witness, so their segwit inputs are told only
// once the block copy replaces them.
func inputAddress(in *wire.TxIn, byID map[chainhash.Hash]*wire.MsgTx) (TxAddress, int64) {
	var prev = in.PreviousOutPoint
	if prev.Index == wire.MaxPrevOutIndex && prev.Hash == (chainhash.Hash{}) {
		return TxAddress{Address: coinbaseInput}, 0
	}
	if tx, ok := byID[prev.Hash]; ok && int(prev.Index) < len(tx.TxOut) {
		var out = tx.TxOut[prev.Index]
		return outputAddress(out.PkScript), out.Value
	}
	return TxAddress{Address: spentAddress(in.SignatureScript, in.Witness)}, 0
}

// historyParams is the network addresses are encoded for.
func historyParams() *chaincfg.Params {
	if params == nil { return &chaincfg.MainNetParams }
	return params
}

// scriptAddress encodes the single address an output script pays, or is
// empty for a script without one.
func scriptAddress(script []byte) string {
	var _, addrs, _, err = txscript.ExtractPkScriptAddrs(script, historyParams())
	if err != nil || len(addrs) != 1 { return "" }
	return addrs[0].EncodeAddress()
}

// spentAddress tells the address of the output an input spends from its
// signature script and witness: P2WPKH and P2WSH, bare or nested in P2SH,
// P2PKH and P2SH. Taproot spends do not reveal the output key, and give an
// empty address, as does anything not recognised.
func spentAddress(sigScript []byte, witness wire.TxWitness) string {
	var net = historyParams()
	var pushes, err = txscript.PushedData(sigScript)
	if err != nil { return "" }
	var encode = func(a btcutil.Address, err error) string {
		if err != nil { return "" }
		return a.EncodeAddress()
	}
	if len(witness) > 0 {
		if len(pushes) == 1 {
			return encode(btcutil.NewAddressScriptHash(pushes[0], net))
		}
		if len(sigScript) > 0 { return "" }
		var last = witness[len(witness)-1]
		if len(witness) == 2 && isPubKey(last) {
			return encode(btcutil.NewAddressWitnessPubKeyHash(btcutil.Hash160(last), net))
		}
		if len(witness) >= 2 && !isControlBlock(last) {
			var hash = chainhash.HashB(last)
			return encode(btcutil.NewAddressWitnessScriptHash(hash, net))
		}
		return ""
	}
	if len(pushes) == 2 && isPubKey(pushes[1]) {
		return encode(btcutil.NewAddressPubKeyHash(btcutil.Hash160(pushes[1]), net))
	}
	if len(pushes) >= 2 {
		return encode(btcutil.NewAddressScriptHash(pushes[len(pushes)-1], net))
	}
	return ""
}

// isPubKey reports whether the data is a compressed or uncompressed public
// key.
func isPubKey(data []byte) bool {
	switch len(data) {
	case 33:
		return data[0] == 0x02 || data[0] == 0x03
	case 65:
		return data[0] == 0x04
	}
	return false
}

// isControlBlock reports whether the data is shaped like the control block
// that ends a taproot script path witness.
func isControlBlock(data []byte) bool {
	return len(data) >= 33 && (len(data)-33)%32 == 0 && data[0]&0xfe == 0xc0
}
