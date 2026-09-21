package spv

import "path/filepath"
import "testing"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
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
