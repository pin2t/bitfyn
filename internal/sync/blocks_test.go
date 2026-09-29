package sync

import "path/filepath"
import "testing"
import "github.com/btcsuite/btcd/blockchain"
import "github.com/btcsuite/btcd/btcutil"
import "github.com/btcsuite/btcd/btcutil/gcs/builder"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/txscript"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/storage"

// testTx builds a transaction spending the outpoint to the output scripts,
// one satoshi amount per script.
func testTx(prev wire.OutPoint, outs ...[]byte) *wire.MsgTx {
	var tx = wire.NewMsgTx(2)
	tx.AddTxIn(wire.NewTxIn(&prev, []byte{0x51}, nil))
	for i, script := range outs {
		tx.AddTxOut(wire.NewTxOut(int64(1000*(i+1)), script))
	}
	return tx
}

// testBlock builds a block holding a coinbase and the transactions, with the
// header committing to them.
func testBlock(txs ...*wire.MsgTx) *wire.MsgBlock {
	var coinbase = testTx(wire.OutPoint{Index: wire.MaxPrevOutIndex}, []byte{0x51})
	var block = wire.NewMsgBlock(&wire.BlockHeader{Version: 1, Bits: easyBits})
	_ = block.AddTransaction(coinbase)
	for _, tx := range txs {
		_ = block.AddTransaction(tx)
	}
	block.Header.MerkleRoot = merkleRoot(block)
	return block
}

func merkleRoot(block *wire.MsgBlock) chainhash.Hash {
	var txs = make([]*btcutil.Tx, len(block.Transactions))
	for i, tx := range block.Transactions {
		txs[i] = btcutil.NewTx(tx)
	}
	return blockchain.CalcMerkleRoot(txs, false)
}

// testFilter builds a basic filter of the block over the given entries.
func testFilter(t *testing.T, block *wire.MsgBlock, entries [][]byte) []byte {
	t.Helper()
	var hash = block.BlockHash()
	var filter, err = builder.WithKeyHash(&hash).AddEntries(entries).Build()
	if err != nil {
		t.Fatalf("build filter: %v", err)
	}
	data, err := filter.NBytes()
	if err != nil {
		t.Fatalf("serialize filter: %v", err)
	}
	return data
}

// TestVerifyBlock checks that a block is accepted only when it hashes to the
// expected block and its transactions match the merkle root without
// duplicates.
func TestVerifyBlock(t *testing.T) {
	var block = testBlock(testTx(wire.OutPoint{Hash: chainhash.Hash{1}}, []byte{0x00, 0x14, 1}))
	if err := verifyBlock(block, block.BlockHash()); err != nil {
		t.Fatalf("valid block rejected: %v", err)
	}
	if err := verifyBlock(block, chainhash.Hash{9}); err == nil {
		t.Fatal("block with unexpected hash accepted")
	}
	var tampered = testBlock(testTx(wire.OutPoint{Hash: chainhash.Hash{1}}, []byte{0x00, 0x14, 1}))
	tampered.Transactions[1].TxOut[0].Value = 1
	if err := verifyBlock(tampered, tampered.BlockHash()); err == nil {
		t.Fatal("block with transactions not matching the merkle root accepted")
	}
	var tx = testTx(wire.OutPoint{Hash: chainhash.Hash{2}}, []byte{0x51})
	var duplicated = testBlock(tx, tx)
	if err := verifyBlock(duplicated, duplicated.BlockHash()); err == nil {
		t.Fatal("block with duplicate transactions accepted")
	}
	var empty = wire.NewMsgBlock(&wire.BlockHeader{})
	if err := verifyBlock(empty, empty.BlockHash()); err == nil {
		t.Fatal("block without transactions accepted")
	}
}

// TestFilterHasOutputs checks the BIP158 output rule: the real basic filter
// passes, a filter omitting an output script fails, and OP_RETURN outputs are
// not required.
func TestFilterHasOutputs(t *testing.T) {
	var a = []byte{0x00, 0x14, 0xaa}
	var b = []byte{0x00, 0x14, 0xbb}
	var nullData = []byte{txscript.OP_RETURN, 0x01, 0x02}
	var block = testBlock(testTx(wire.OutPoint{Hash: chainhash.Hash{1}}, a, b, nullData))
	var hash = block.BlockHash()
	var full, err = builder.BuildBasicFilter(block, nil)
	if err != nil {
		t.Fatalf("BuildBasicFilter: %v", err)
	}
	fullData, err := full.NBytes()
	if err != nil {
		t.Fatalf("NBytes: %v", err)
	}
	if ok, err := filterHasOutputs(fullData, &hash, block); err != nil || !ok {
		t.Fatalf("basic filter rejected: %v, %v", ok, err)
	}
	var missing = testFilter(t, block, [][]byte{{0x51}, a})
	if ok, err := filterHasOutputs(missing, &hash, block); err != nil || ok {
		t.Fatalf("filter missing an output accepted: %v, %v", ok, err)
	}
}

// TestResolveVariants checks that the mismatch resolution drops filters
// missing block outputs, prefers the valid variant with most peers and fails
// when no variant is valid.
func TestResolveVariants(t *testing.T) {
	var a = []byte{0x00, 0x14, 0xaa}
	var extra = []byte{0x00, 0x14, 0xee}
	var block = testBlock(testTx(wire.OutPoint{Hash: chainhash.Hash{1}}, a))
	var good = testFilter(t, block, [][]byte{{0x51}, a})
	var goodExtra = testFilter(t, block, [][]byte{{0x51}, a, extra})
	var bad = testFilter(t, block, [][]byte{{0x51}})
	var p1 = &conn{addr: "p1"}
	var p2 = &conn{addr: "p2"}
	var p3 = &conn{addr: "p3"}
	var winner, valid, err = resolveVariants([]*variant{
		{data: bad, peers: []*conn{p1, p2}},
		{data: good, peers: []*conn{p3}},
	}, block)
	if err != nil || winner != 1 || valid[0] || !valid[1] {
		t.Fatalf("outvoted honest filter: winner %d, valid %v, err %v", winner, valid, err)
	}
	winner, _, err = resolveVariants([]*variant{
		{data: good, peers: []*conn{p1}},
		{data: goodExtra, peers: []*conn{p2, p3}},
	}, block)
	if err != nil || winner != 1 {
		t.Fatalf("two valid variants: winner %d, err %v; want the majority 1", winner, err)
	}
	winner, _, err = resolveVariants([]*variant{
		{data: good, peers: []*conn{p1}},
		{data: goodExtra, peers: []*conn{p2}},
	}, block)
	if err != nil || winner != 0 {
		t.Fatalf("tied valid variants: winner %d, err %v; want the first", winner, err)
	}
	if _, _, err = resolveVariants([]*variant{{data: bad, peers: []*conn{p1}}}, block); err == nil {
		t.Fatal("resolution without a valid filter succeeded")
	}
}

// TestGroupVariants checks that identical filters from several peers form
// one variant, in peer order.
func TestGroupVariants(t *testing.T) {
	var p1 = &conn{addr: "p1"}
	var p2 = &conn{addr: "p2"}
	var p3 = &conn{addr: "p3"}
	var got = [][][]byte{
		{{1}, {7}},
		{{2}, {7}},
		{{1}, {7}},
	}
	var first = groupVariants([]*conn{p1, p2, p3}, got, 0)
	if len(first) != 2 || len(first[0].peers) != 2 || first[0].peers[1] != p3 || first[1].peers[0] != p2 {
		t.Fatalf("height 0 variants = %+v", first)
	}
	if second := groupVariants([]*conn{p1, p2, p3}, got, 1); len(second) != 1 || len(second[0].peers) != 3 {
		t.Fatalf("height 1 variants = %+v", second)
	}
}

// TestProcessBlock checks that wallet receives and spends are stored, other
// transactions are ignored, and the wallet coins survive a reload.
func TestProcessBlock(t *testing.T) {
	resetSync()
	var st, err = storage.Open(filepath.Join(t.TempDir(), "w.db"), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	store = st
	var mine = []byte{0x00, 0x14, 0x01, 0x02}
	scripts = []watchScript{{address: "bc1mine", script: mine}}
	scriptIndex = map[string]int{string(mine): 0}
	var receive = testTx(wire.OutPoint{Hash: chainhash.Hash{1}}, []byte{0x51}, mine)
	var other = testTx(wire.OutPoint{Hash: chainhash.Hash{2}}, []byte{0x52})
	var n, perr = processBlock(5, testBlock(receive, other))
	if perr != nil || n != 1 {
		t.Fatalf("receive block: %d, %v; want 1 wallet tx", n, perr)
	}
	var coin = wire.OutPoint{Hash: receive.TxHash(), Index: 1}
	if outpoints[coin] != "bc1mine" {
		t.Fatalf("wallet coin %s not tracked: %v", coin, outpoints)
	}
	var spend = testTx(coin, []byte{0x53})
	n, perr = processBlock(6, testBlock(spend))
	if perr != nil || n != 1 {
		t.Fatalf("spend block: %d, %v; want 1 wallet tx", n, perr)
	}
	var txs, terr = st.Transactions()
	if terr != nil || len(txs) != 2 || txs[0].Txid != receive.TxHash() || txs[1].Txid != spend.TxHash() || txs[1].Height != 6 {
		t.Fatalf("stored transactions = %+v, %v", txs, terr)
	}
	outpoints = nil
	if err := loadOutpoints(); err != nil {
		t.Fatalf("loadOutpoints: %v", err)
	}
	if len(outpoints) != 1 || outpoints[coin] != "bc1mine" {
		t.Fatalf("reloaded coins = %v", outpoints)
	}
}
