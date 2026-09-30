package wallet

import "errors"
import "testing"
import "github.com/btcsuite/btcd/blockchain"
import "github.com/btcsuite/btcd/btcutil"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/txscript"
import "github.com/btcsuite/btcd/wire"

// testCoin derives the coin of value at the receive or change index.
func testCoin(t *testing.T, w *Wallet, chain, index uint32, value int64, confirmed bool) Coin {
	t.Helper()
	var addr, path, _, err = w.deriveOn(chain, index)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	script, err := w.AddressScript(addr)
	if err != nil {
		t.Fatalf("AddressScript: %v", err)
	}
	return Coin{
		OutPoint:  wire.OutPoint{Hash: chainhash.Hash{byte(chain), byte(index), byte(value)}, Index: index},
		Value:     value,
		PkScript:  script,
		Address:   addr,
		Path:      path,
		Confirmed: confirmed,
	}
}

func testWallet(t *testing.T) *Wallet {
	t.Helper()
	var w, err = New(vectorMnemonic, "", &chaincfg.RegressionNetParams)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return w
}

// TestChangeAddress checks that change addresses come from the internal
// chain, against the first change address of the BIP84 test vector.
func TestChangeAddress(t *testing.T) {
	var w, err = New(vectorMnemonic, "", &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var change, path, _, cerr = w.DeriveChangeAddress(0)
	if cerr != nil {
		t.Fatalf("DeriveChangeAddress: %v", cerr)
	}
	if path != "m/84'/0'/0'/1/0" {
		t.Errorf("path = %s, want m/84'/0'/0'/1/0", path)
	}
	if change != "bc1q8c6fshw2dlwun7ekn9qwf37cu2rn755upcp6el" {
		t.Errorf("change address = %s", change)
	}
}

// TestSignSpend signs a two-input spend and runs every input through the
// script engine, and checks the size estimate never falls short.
func TestSignSpend(t *testing.T) {
	var w = testWallet(t)
	var coins = []Coin{testCoin(t, w, ReceiveChain, 0, 60_000, true), testCoin(t, w, ChangeChain, 2, 50_000, false)}
	var dest, _ = w.AddressScript("bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080")
	var changeAddr, _, _, _ = w.DeriveChangeAddress(3)
	var change, _ = w.AddressScript(changeAddr)
	var spend, err = PlanSpend(coins, true, dest, 80_000, 5, change)
	if err != nil {
		t.Fatalf("PlanSpend: %v", err)
	}
	tx, err := w.SignSpend(spend, dest, change)
	if err != nil {
		t.Fatalf("SignSpend: %v", err)
	}
	var prevOuts = make(map[wire.OutPoint]*wire.TxOut)
	for _, c := range coins {
		prevOuts[c.OutPoint] = wire.NewTxOut(c.Value, c.PkScript)
	}
	var fetcher = txscript.NewMultiPrevOutFetcher(prevOuts)
	var hashes = txscript.NewTxSigHashes(tx, fetcher)
	for i, in := range tx.TxIn {
		var prev = prevOuts[in.PreviousOutPoint]
		var vm, verr = txscript.NewEngine(prev.PkScript, tx, i, txscript.StandardVerifyFlags, nil, hashes, prev.Value, fetcher)
		if verr != nil {
			t.Fatalf("engine for input %d: %v", i, verr)
		}
		if err := vm.Execute(); err != nil {
			t.Fatalf("input %d does not verify: %v", i, err)
		}
		if in.Sequence != sequenceRBF {
			t.Errorf("input %d does not signal RBF", i)
		}
	}
	var out = int64(0)
	for _, o := range tx.TxOut {
		out += o.Value
	}
	if out+spend.Fee != 110_000 || len(tx.TxOut) != 2 {
		t.Fatalf("outputs %d + fee %d != inputs 110000, %d outputs", out, spend.Fee, len(tx.TxOut))
	}
	var actual = (blockchain.GetTransactionWeight(btcutil.NewTx(tx)) + 3) / 4
	var estimate = EstimateVSize(2, [][]byte{dest, change})
	if estimate < actual || estimate > actual+2 {
		t.Fatalf("estimated %d vB, actual %d vB", estimate, actual)
	}
}

// TestPlanSpend checks automatic coin selection, the dust change rule, the
// dust amount rule, manual selection and missing funds.
func TestPlanSpend(t *testing.T) {
	var w = testWallet(t)
	var big = testCoin(t, w, ReceiveChain, 0, 100_000, true)
	var small = testCoin(t, w, ReceiveChain, 1, 20_000, true)
	var unconfirmed = testCoin(t, w, ReceiveChain, 2, 500_000, false)
	var dest, _ = w.AddressScript("bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080")
	var change = big.PkScript
	var s, err = PlanSpend([]Coin{small, unconfirmed, big}, false, dest, 50_000, 2, change)
	if err != nil || len(s.Inputs) != 1 || s.Inputs[0].OutPoint != big.OutPoint {
		t.Fatalf("auto selection = %+v, %v; want the big confirmed coin", s.Inputs, err)
	}
	if s.Fee != EstimateVSize(1, [][]byte{dest, change})*2 || s.Change != 100_000-50_000-s.Fee {
		t.Fatalf("fee %d change %d", s.Fee, s.Change)
	}
	var nearAll = 100_000 - EstimateVSize(1, [][]byte{dest})*2 - 100
	s, err = PlanSpend([]Coin{big}, false, dest, nearAll, 2, change)
	if err != nil || s.Change != 0 || s.Fee != 100_000-nearAll {
		t.Fatalf("dust change: %+v, %v; want no change, remainder to fee", s, err)
	}
	if _, err = PlanSpend([]Coin{big}, false, dest, 100, 2, change); err == nil {
		t.Fatal("dust amount accepted")
	}
	s, err = PlanSpend([]Coin{small, big}, true, dest, 10_000, 2, change)
	if err != nil || len(s.Inputs) != 2 {
		t.Fatalf("manual selection = %+v, %v; want both coins", s.Inputs, err)
	}
	if _, err = PlanSpend([]Coin{small}, true, dest, 50_000, 2, change); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("missing funds: %v", err)
	}
	var max = MaxAmount([]Coin{small, big}, dest, 2)
	s, err = PlanSpend([]Coin{small, big}, true, dest, max, 2, change)
	if err != nil || s.Change != 0 || s.Fee != EstimateVSize(2, [][]byte{dest})*2 {
		t.Fatalf("max amount %d: %+v, %v", max, s, err)
	}
}

// TestAddressScript checks destination validation per network.
func TestAddressScript(t *testing.T) {
	var w = testWallet(t)
	if _, err := w.AddressScript("bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu"); err == nil {
		t.Error("mainnet address accepted on regtest")
	}
	if _, err := w.AddressScript("not an address"); err == nil {
		t.Error("garbage accepted")
	}
	if script, err := w.AddressScript(" bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080 "); err != nil || len(script) != 22 {
		t.Errorf("regtest P2WPKH: %x, %v", script, err)
	}
	var p2wpkh = append([]byte{0x00, 0x14}, make([]byte, 20)...)
	if DustLimit(p2wpkh) != 294 {
		t.Errorf("P2WPKH dust limit = %d, want 294", DustLimit(p2wpkh))
	}
	var p2pkh = append(append([]byte{0x76, 0xa9, 0x14}, make([]byte, 20)...), 0x88, 0xac)
	if DustLimit(p2pkh) != 546 {
		t.Errorf("P2PKH dust limit = %d, want 546", DustLimit(p2pkh))
	}
}
