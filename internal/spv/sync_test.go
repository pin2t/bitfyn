package spv

import "path/filepath"
import "testing"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/storage"

// TestCheckFilterPrev checks the cfheaders chain-linkage rule: the null hash
// (and the genesis hash) is accepted for the first batch, stored headers are
// required for later batches.
func TestCheckFilterPrev(t *testing.T) {
	var path = filepath.Join(t.TempDir(), "w.db")
	var store, err = storage.Open(path, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	var stored = chainhash.Hash{1, 2, 3}
	if err := store.SaveFilter(storage.Filter{Height: 0, BlockHash: chainhash.Hash{4}, FilterHeader: stored, Data: []byte{1}}); err != nil {
		t.Fatalf("SaveFilter: %v", err)
	}
	var params = &chaincfg.MainNetParams
	var syncer = &Syncer{params: params, store: store}
	if err := syncer.checkFilterPrev(0, chainhash.Hash{}); err != nil {
		t.Fatalf("null prev at height 0 rejected: %v", err)
	}
	if err := syncer.checkFilterPrev(0, *params.GenesisHash); err != nil {
		t.Fatalf("genesis prev at height 0 rejected: %v", err)
	}
	if err := syncer.checkFilterPrev(0, chainhash.Hash{9}); err == nil {
		t.Fatal("unknown prev at height 0 accepted")
	}
	if err := syncer.checkFilterPrev(1, stored); err != nil {
		t.Fatalf("stored prev at height 1 rejected: %v", err)
	}
	if err := syncer.checkFilterPrev(1, chainhash.Hash{}); err == nil {
		t.Fatal("null prev at height 1 accepted")
	}
}

// TestStoreFilterChainedHeaders builds a two-block chain and verifies that a
// downloaded filter is stored with its chained header and that mismatched
// data is rejected.
func TestStoreFilterChainedHeaders(t *testing.T) {
	var path = filepath.Join(t.TempDir(), "w.db")
	var store, err = storage.Open(path, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	var params = &chaincfg.MainNetParams
	var chain = NewChain(params)
	if err := chain.Add(&params.GenesisBlock.Header); err != nil {
		t.Fatalf("add genesis: %v", err)
	}
	var ts = params.GenesisBlock.Header.Timestamp.Add(time.Minute)
	var hdr = mineHeader(params.GenesisBlock.Header.BlockHash(), easyBits, ts)
	if err := chain.Add(&hdr); err != nil {
		t.Fatalf("add header 1: %v", err)
	}
	var syncer = &Syncer{params: params, store: store, chain: chain, pending: make(map[int32]chainhash.Hash)}
	var data0 = []byte{0x0a, 0x0b}
	var raw0 = filterHash(data0)
	var chained0 = filterHeader(raw0, chainhash.Hash{})
	var genHash = params.GenesisBlock.Header.BlockHash()
	if err := store.SaveFilter(storage.Filter{Height: 0, BlockHash: genHash, FilterHeader: chained0, Data: data0}); err != nil {
		t.Fatalf("SaveFilter 0: %v", err)
	}
	var data1 = []byte{0x0c, 0x0d}
	var raw1 = filterHash(data1)
	syncer.pending[1] = raw1
	var block1 = hdr.BlockHash()
	if err := syncer.storeFilter(&wire.MsgCFilter{FilterType: wire.GCSFilterRegular, BlockHash: block1, Data: data1}); err != nil {
		t.Fatalf("storeFilter: %v", err)
	}
	var stored, ok, herr = store.FilterHeaderAt(1)
	if herr != nil || !ok {
		t.Fatalf("FilterHeaderAt(1) = %s, %v, %v", stored, ok, herr)
	}
	var want1 = filterHeader(raw1, chained0)
	if stored != want1 {
		t.Fatalf("stored chained header = %x, want %x", stored, want1)
	}
	if err := syncer.storeFilter(&wire.MsgCFilter{FilterType: wire.GCSFilterRegular, BlockHash: block1, Data: []byte{0xff}}); err == nil {
		t.Fatal("filter with mismatched data accepted")
	}
}
