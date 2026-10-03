package sync

import "bytes"
import "testing"
import "github.com/btcsuite/btcd/btcutil"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/txscript"
import "github.com/btcsuite/btcd/wire"

// testPubKey is a compressed public key shaped value: addresses only hash it.
var testPubKey = append([]byte{0x02}, bytes.Repeat([]byte{9}, 32)...)

// TestSpentAddress checks the addresses told from signature scripts and
// witnesses of the common script types, and the ones that cannot be told.
func TestSpentAddress(t *testing.T) {
	resetSync()
	params = &chaincfg.RegressionNetParams
	var net = params
	var hash = btcutil.Hash160(testPubKey)
	var wpkh, _ = btcutil.NewAddressWitnessPubKeyHash(hash, net)
	var pkh, _ = btcutil.NewAddressPubKeyHash(hash, net)
	var redeem = append([]byte{0x00, 0x14}, hash...)
	var nested, _ = btcutil.NewAddressScriptHash(redeem, net)
	var witnessScript = []byte{0x51, 0x21}
	var wsh, _ = btcutil.NewAddressWitnessScriptHash(chainhash.HashB(witnessScript), net)
	var push = func(data ...[]byte) []byte {
		var b = txscript.NewScriptBuilder()
		for _, d := range data {
			b.AddData(d)
		}
		var script, _ = b.Script()
		return script
	}
	var sig = bytes.Repeat([]byte{0x30}, 71)
	var cases = []struct {
		name      string
		sigScript []byte
		witness   wire.TxWitness
		want      string
	}{
		{"p2wpkh", nil, wire.TxWitness{sig, testPubKey}, wpkh.EncodeAddress()},
		{"p2sh-p2wpkh", push(redeem), wire.TxWitness{sig, testPubKey}, nested.EncodeAddress()},
		{"p2wsh", nil, wire.TxWitness{{}, sig, witnessScript}, wsh.EncodeAddress()},
		{"p2pkh", push(sig, testPubKey), nil, pkh.EncodeAddress()},
		{"taproot key path", nil, wire.TxWitness{bytes.Repeat([]byte{1}, 64)}, ""},
		{"taproot script path", nil, wire.TxWitness{sig, {0x51}, append([]byte{0xc0}, bytes.Repeat([]byte{2}, 32)...)}, ""},
		{"p2pk", push(sig), nil, ""},
		{"nothing", nil, nil, ""},
	}
	for _, c := range cases {
		if got := spentAddress(c.sigScript, c.witness); got != c.want {
			t.Errorf("%s: spentAddress = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestHistory checks the wallet transactions newest first: a confirmed
// payment received from a foreign P2WPKH input, then a pending spend of it
// with no change, with their net amounts, addresses and which are the
// wallet's.
func TestHistory(t *testing.T) {
	var mine = testWallet(t)
	params = &chaincfg.RegressionNetParams
	var hash = btcutil.Hash160(testPubKey)
	var payer, _ = btcutil.NewAddressWitnessPubKeyHash(hash, params)
	var other = append([]byte{0x00, 0x14}, bytes.Repeat([]byte{8}, 20)...)
	var otherAddr = scriptAddress(other)
	var receive = testTx(wire.OutPoint{Hash: chainhash.Hash{1}}, other, mine)
	receive.TxIn[0].SignatureScript = nil
	receive.TxIn[0].Witness = wire.TxWitness{bytes.Repeat([]byte{0x30}, 71), testPubKey}
	if _, err := processBlock(1, testBlock(receive)); err != nil {
		t.Fatalf("processBlock: %v", err)
	}
	var spend = testTx(wire.OutPoint{Hash: receive.TxHash(), Index: 1}, other)
	acceptPendingTx(&conn{addr: "p"}, spend)
	var list = History()
	if len(list) != 2 {
		t.Fatalf("%d transactions, want 2: %+v", len(list), list)
	}
	var sent, got = list[0], list[1]
	if sent.Txid != spend.TxHash() || sent.Confirmed || sent.Net != -2000 {
		t.Errorf("newest = %s confirmed %v net %d, want the pending spend of -2000", sent.Txid, sent.Confirmed, sent.Net)
	}
	if len(sent.Inputs) != 1 || sent.Inputs[0] != (TxAddress{Address: "bc1mine", Mine: true}) {
		t.Errorf("spend inputs = %+v, want the wallet address", sent.Inputs)
	}
	if len(sent.Outputs) != 1 || sent.Outputs[0] != (TxAddress{Address: otherAddr}) || otherAddr == "" {
		t.Errorf("spend outputs = %+v, want %q", sent.Outputs, otherAddr)
	}
	if got.Txid != receive.TxHash() || !got.Confirmed || got.Net != 2000 {
		t.Errorf("oldest = %s confirmed %v net %d, want the confirmed receive of 2000", got.Txid, got.Confirmed, got.Net)
	}
	if len(got.Inputs) != 1 || got.Inputs[0] != (TxAddress{Address: payer.EncodeAddress()}) {
		t.Errorf("receive inputs = %+v, want the payer %s", got.Inputs, payer.EncodeAddress())
	}
	var wantOut = []TxAddress{{Address: otherAddr}, {Address: "bc1mine", Mine: true}}
	if len(got.Outputs) != 2 || got.Outputs[0] != wantOut[0] || got.Outputs[1] != wantOut[1] {
		t.Errorf("receive outputs = %+v, want %+v", got.Outputs, wantOut)
	}
	var coinbase = buildHistory([]*wire.MsgTx{testTx(wire.OutPoint{Index: wire.MaxPrevOutIndex}, mine)}, []int64{0}, 1)
	if coinbase[0].Inputs[0].Address != coinbaseInput || coinbase[0].Net != 1000 {
		t.Errorf("coinbase = %+v", coinbase[0])
	}
}
