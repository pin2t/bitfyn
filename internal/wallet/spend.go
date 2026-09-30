package wallet

import "errors"
import "fmt"
import "sort"
import "strconv"
import "strings"
import "github.com/btcsuite/btcd/btcutil"
import "github.com/btcsuite/btcd/btcutil/hdkeychain"
import "github.com/btcsuite/btcd/btcutil/txsort"
import "github.com/btcsuite/btcd/txscript"
import "github.com/btcsuite/btcd/wire"

// ErrInsufficientFunds is returned when the coins cannot pay the amount and
// the fee.
var ErrInsufficientFunds = errors.New("insufficient funds")

// Coin is one spendable wallet output with the derivation path of the key
// that controls it, and the unix time it appeared: the time of its block, or
// when its unconfirmed transaction was first seen. Time is 0 when unknown.
type Coin struct {
	OutPoint  wire.OutPoint
	Value     int64
	PkScript  []byte
	Address   string
	Path      string
	Confirmed bool
	Time      int64
}

// Spend is a planned transaction: the coins it spends, the amount paid, the
// fee and the change returned to the wallet, all in satoshis. Change is zero
// when it would be dust; the remainder then goes to the fee.
type Spend struct {
	Inputs []Coin
	Amount int64
	Fee    int64
	Change int64
}

// Transaction weights in weight units: the fixed part of a segwit
// transaction, and one P2WPKH input with a worst-case 72-byte signature.
const txOverheadWeight = (4 + 4 + 1 + 1) * 4 + 2
const p2wpkhInputWeight = (32+4+1+4)*4 + 1 + 1 + 72 + 1 + 33

// sequenceRBF signals BIP125 replaceability, so a stuck payment can be
// bumped later.
const sequenceRBF = wire.MaxTxInSequenceNum - 2

// EstimateVSize returns the virtual size in vbytes of a transaction
// spending that many P2WPKH inputs to the output scripts. It never
// underestimates: signatures are counted at their maximum length.
func EstimateVSize(inputs int, outputs [][]byte) int64 {
	var weight = int64(txOverheadWeight + inputs*p2wpkhInputWeight)
	for _, script := range outputs {
		weight += int64(8+wire.VarIntSerializeSize(uint64(len(script)))+len(script)) * 4
	}
	return (weight + 3) / 4
}

// DustLimit is the smallest output value Bitcoin Core relays for the script
// at its default dust relay fee of 3 sat/vB: the cost of creating and later
// spending the output.
func DustLimit(script []byte) int64 {
	var spend = int64(32 + 4 + 1 + 107 + 4)
	if txscript.IsWitnessProgram(script) {
		spend = 32 + 4 + 1 + 107/4 + 4
	}
	return (int64(8+wire.VarIntSerializeSize(uint64(len(script)))+len(script)) + spend) * 3
}

// PlanSpend plans paying amount to the destination script at the fee rate
// in sat/vB. With useAll every given coin is spent, as when the user picked
// them; otherwise coins are added largest first, confirmed ones before
// unconfirmed, until they cover the amount and the fee. Change goes to the
// change script unless it would be dust.
func PlanSpend(coins []Coin, useAll bool, dest []byte, amount, feeRate int64, change []byte) (Spend, error) {
	if amount < DustLimit(dest) {
		return Spend{}, fmt.Errorf("amount %d sat is below the %d sat dust limit", amount, DustLimit(dest))
	}
	if feeRate < 1 {
		return Spend{}, fmt.Errorf("fee rate must be at least 1 sat/vB")
	}
	var candidates = append([]Coin(nil), coins...)
	if !useAll {
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].Confirmed != candidates[j].Confirmed {
				return candidates[i].Confirmed
			}
			return candidates[i].Value > candidates[j].Value
		})
	}
	var start = 1
	if useAll { start = len(candidates) }
	for n := start; n <= len(candidates); n++ {
		var inputs = candidates[:n]
		var sum = int64(0)
		for _, c := range inputs {
			sum += c.Value
		}
		var withChange = EstimateVSize(n, [][]byte{dest, change}) * feeRate
		if rest := sum - amount - withChange; rest >= DustLimit(change) {
			return Spend{Inputs: inputs, Amount: amount, Fee: withChange, Change: rest}, nil
		}
		var noChange = EstimateVSize(n, [][]byte{dest}) * feeRate
		if sum-amount >= noChange {
			return Spend{Inputs: inputs, Amount: amount, Fee: sum - amount}, nil
		}
	}
	return Spend{}, ErrInsufficientFunds
}

// MaxAmount is the most the coins can pay to the destination at the fee
// rate, with no change: their value minus the fee, or 0 when that is dust.
func MaxAmount(coins []Coin, dest []byte, feeRate int64) int64 {
	var sum = int64(0)
	for _, c := range coins {
		sum += c.Value
	}
	var amount = sum - EstimateVSize(len(coins), [][]byte{dest})*feeRate
	if amount < DustLimit(dest) {
		return 0
	}
	return amount
}

// SignSpend builds the planned transaction paying the destination script,
// with change to the change script, and signs every input with the key at
// the coin's derivation path. Inputs and outputs are sorted per BIP69, so
// the change position reveals nothing, and every input signals RBF.
func (w *Wallet) SignSpend(s Spend, dest, change []byte) (*wire.MsgTx, error) {
	var tx = wire.NewMsgTx(2)
	var prevOuts = make(map[wire.OutPoint]*wire.TxOut, len(s.Inputs))
	var coins = make(map[wire.OutPoint]Coin, len(s.Inputs))
	for _, c := range s.Inputs {
		var in = wire.NewTxIn(&c.OutPoint, nil, nil)
		in.Sequence = sequenceRBF
		tx.AddTxIn(in)
		prevOuts[c.OutPoint] = wire.NewTxOut(c.Value, c.PkScript)
		coins[c.OutPoint] = c
	}
	tx.AddTxOut(wire.NewTxOut(s.Amount, dest))
	if s.Change > 0 {
		tx.AddTxOut(wire.NewTxOut(s.Change, change))
	}
	txsort.InPlaceSort(tx)
	var fetcher = txscript.NewMultiPrevOutFetcher(prevOuts)
	var hashes = txscript.NewTxSigHashes(tx, fetcher)
	for i, in := range tx.TxIn {
		var c = coins[in.PreviousOutPoint]
		var key, err = w.keyForPath(c.Path)
		if err != nil { return nil, fmt.Errorf("key for %s: %w", c.Address, err) }
		priv, err := key.ECPrivKey()
		if err != nil { return nil, fmt.Errorf("private key for %s: %w", c.Address, err) }
		witness, err := txscript.WitnessSignature(tx, hashes, i, c.Value, c.PkScript, txscript.SigHashAll, priv, true)
		if err != nil { return nil, fmt.Errorf("sign input %d: %w", i, err) }
		in.Witness = witness
	}
	return tx, nil
}

// AddressScript decodes a destination address for the wallet's network and
// returns its output script.
func (w *Wallet) AddressScript(address string) ([]byte, error) {
	var addr, err = btcutil.DecodeAddress(strings.TrimSpace(address), w.net)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}
	if !addr.IsForNet(w.net) {
		return nil, fmt.Errorf("address is not for %s", w.net.Name)
	}
	return txscript.PayToAddrScript(addr)
}

// keyForPath derives the extended key of a BIP32 path such as
// m/84'/0'/0'/1/3.
func (w *Wallet) keyForPath(path string) (*hdkeychain.ExtendedKey, error) {
	var parts = strings.Split(path, "/")
	if len(parts) < 2 || parts[0] != "m" {
		return nil, fmt.Errorf("invalid derivation path %q", path)
	}
	var steps = make([]uint32, 0, len(parts)-1)
	for _, part := range parts[1:] {
		var hardened = strings.HasSuffix(part, "'")
		var n, err = strconv.ParseUint(strings.TrimSuffix(part, "'"), 10, 31)
		if err != nil {
			return nil, fmt.Errorf("invalid derivation path %q", path)
		}
		var step = uint32(n)
		if hardened { step = harden(step) }
		steps = append(steps, step)
	}
	return w.derivePath(steps)
}
