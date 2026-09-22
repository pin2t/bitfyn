package spv

import "path/filepath"
import "testing"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/storage"

// resetSync clears the package-level sync state so each test starts fresh.
func resetSync() {
	params = nil
	store = nil
	chain = nil
	scripts = nil
	progress = nil
	stats = nil
	hdrCh = nil
	cfhdrCh = nil
	fltCh = nil
	addrCh = nil
	pending = nil
	seedTime = 0
	filterStart = 0
	anchorPrev = chainhash.Hash{}
	anchorSet = false
}

// TestCheckFilterPrev checks the cfheaders chain-linkage rule: the null hash
// (and the genesis hash) is accepted for the first batch, stored headers are
// required for later batches.
func TestCheckFilterPrev(t *testing.T) {
	resetSync()
	var path = filepath.Join(t.TempDir(), "w.db")
	var st, err = storage.Open(path, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	var stored = chainhash.Hash{1, 2, 3}
	if err := st.SaveFilter(storage.Filter{Height: 0, BlockHash: chainhash.Hash{4}, FilterHeader: stored, Data: []byte{1}}); err != nil {
		t.Fatalf("SaveFilter: %v", err)
	}
	params = &chaincfg.MainNetParams
	store = st
	if err := checkFilterPrev(0, chainhash.Hash{}); err != nil {
		t.Fatalf("null prev at height 0 rejected: %v", err)
	}
	if err := checkFilterPrev(0, *params.GenesisHash); err != nil {
		t.Fatalf("genesis prev at height 0 rejected: %v", err)
	}
	if err := checkFilterPrev(0, chainhash.Hash{9}); err == nil {
		t.Fatal("unknown prev at height 0 accepted")
	}
	if err := checkFilterPrev(1, stored); err != nil {
		t.Fatalf("stored prev at height 1 rejected: %v", err)
	}
	if err := checkFilterPrev(1, chainhash.Hash{}); err == nil {
		t.Fatal("null prev at height 1 accepted")
	}
}

// TestFirstHeaderAtOrAfter checks that the wallet seed time maps to the first
// block whose time is at or after it, and that the filter start height sits
// safetyGap blocks earlier, with genesis and future fallbacks.
func TestFirstHeaderAtOrAfter(t *testing.T) {
	var netParams = &chaincfg.MainNetParams
	var ch = NewChain(netParams)
	if err := ch.Add(&netParams.GenesisBlock.Header); err != nil {
		t.Fatalf("add genesis: %v", err)
	}
	var ts = netParams.GenesisBlock.Header.Timestamp.Add(time.Minute)
	var prev = netParams.GenesisBlock.Header.BlockHash()
	for height := int32(1); height <= 12; height++ {
		var hdr = mineHeader(prev, easyBits, ts)
		if err := ch.Add(&hdr); err != nil {
			t.Fatalf("add header %d: %v", height, err)
		}
		prev = hdr.BlockHash()
		ts = ts.Add(time.Minute)
	}
	if got := firstHeaderAtOrAfter(ch, 0); got != 0 {
		t.Fatalf("firstHeaderAtOrAfter(0) = %d, want 0", got)
	}
	if got := firstHeaderAtOrAfter(ch, netParams.GenesisBlock.Header.Timestamp.Unix()); got != 0 {
		t.Fatalf("firstHeaderAtOrAfter(genesis time) = %d, want 0", got)
	}
	if got := firstHeaderAtOrAfter(ch, netParams.GenesisBlock.Header.Timestamp.Add(3*time.Minute).Unix()); got != 3 {
		t.Fatalf("firstHeaderAtOrAfter(block 3 time) = %d, want 3", got)
	}
	if got := firstHeaderAtOrAfter(ch, netParams.GenesisBlock.Header.Timestamp.Add(12*time.Minute).Unix()); got != 12 {
		t.Fatalf("firstHeaderAtOrAfter(block 12 time) = %d, want 12", got)
	}
	if got := firstHeaderAtOrAfter(ch, ts.Add(time.Hour).Unix()); got != 13 {
		t.Fatalf("firstHeaderAtOrAfter(future time) = %d, want 13", got)
	}
	if got := filterStartHeight(ch, 0); got != 0 {
		t.Fatalf("filterStartHeight(0) = %d, want 0", got)
	}
	if got := filterStartHeight(ch, netParams.GenesisBlock.Header.Timestamp.Add(3*time.Minute).Unix()); got != 0 {
		t.Fatalf("filterStartHeight(block 3 time) = %d, want 0", got)
	}
	if got := filterStartHeight(ch, netParams.GenesisBlock.Header.Timestamp.Add(12*time.Minute).Unix()); got != 2 {
		t.Fatalf("filterStartHeight(block 12 time) = %d, want 2", got)
	}
}

// TestMajorityAnchor checks the strict majority rule for the filter header
// anchor votes.
func TestMajorityAnchor(t *testing.T) {
	var a = chainhash.Hash{1}
	var b = chainhash.Hash{2}
	var c = chainhash.Hash{3}
	if _, err := MajorityAnchor(nil); err == nil {
		t.Fatal("empty votes accepted")
	}
	var got, err = MajorityAnchor([]chainhash.Hash{a, a, b})
	if err != nil || got != a {
		t.Fatalf("majority of [a a b] = %s, %v; want a", got, err)
	}
	got, err = MajorityAnchor([]chainhash.Hash{a})
	if err != nil || got != a {
		t.Fatalf("single vote = %s, %v; want a", got, err)
	}
	if _, err = MajorityAnchor([]chainhash.Hash{a, b}); err == nil {
		t.Fatal("tie accepted")
	}
	if _, err = MajorityAnchor([]chainhash.Hash{a, a, b, b, c}); err == nil {
		t.Fatal("plurality without majority accepted")
	}
}

// TestCheckFilterPrevAnchor checks that the first cfheaders batch at the
// wallet seed height trusts the peer's prev_filter_header as the chain anchor
// and records it, while later batches still verify against the stored chain.
// A majority-proven anchor must match exactly.
func TestCheckFilterPrevAnchor(t *testing.T) {
	resetSync()
	var path = filepath.Join(t.TempDir(), "w.db")
	var st, err = storage.Open(path, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	params = &chaincfg.MainNetParams
	store = st
	filterStart = 5
	var anchor = chainhash.Hash{1, 2, 3}
	if err := checkFilterPrev(5, anchor); err != nil {
		t.Fatalf("anchor batch rejected: %v", err)
	}
	if anchorPrev != anchor {
		t.Fatalf("anchorPrev = %s, want %s", anchorPrev, anchor)
	}
	if err := checkFilterPrev(6, chainhash.Hash{9}); err == nil {
		t.Fatal("batch after anchor accepted without stored predecessor")
	}
	if err := st.SaveFilter(storage.Filter{Height: 5, BlockHash: chainhash.Hash{4}, FilterHeader: anchor, Data: []byte{1}}); err != nil {
		t.Fatalf("SaveFilter: %v", err)
	}
	if err := checkFilterPrev(6, anchor); err != nil {
		t.Fatalf("batch after anchor with stored predecessor rejected: %v", err)
	}
	SetFilterAnchor(anchor)
	if err := checkFilterPrev(5, anchor); err != nil {
		t.Fatalf("matching proven anchor rejected: %v", err)
	}
	if err := checkFilterPrev(5, chainhash.Hash{9}); err == nil {
		t.Fatal("peer anchor differing from the proven majority accepted")
	}
}

// TestStoreFilterAnchorPrev checks that a filter stored at the wallet seed
// height chains to the trusted anchor, and that its data is pruned while the
// chained header stays queryable.
func TestStoreFilterAnchorPrev(t *testing.T) {
	resetSync()
	var path = filepath.Join(t.TempDir(), "w.db")
	var st, err = storage.Open(path, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	params = &chaincfg.MainNetParams
	store = st
	chain = NewChain(params)
	if err := chain.Add(&params.GenesisBlock.Header); err != nil {
		t.Fatalf("add genesis: %v", err)
	}
	var ts = params.GenesisBlock.Header.Timestamp.Add(time.Minute)
	var hdr1 = mineHeader(params.GenesisBlock.Header.BlockHash(), easyBits, ts)
	if err := chain.Add(&hdr1); err != nil {
		t.Fatalf("add header 1: %v", err)
	}
	var hdr2 = mineHeader(hdr1.BlockHash(), easyBits, ts.Add(time.Minute))
	if err := chain.Add(&hdr2); err != nil {
		t.Fatalf("add header 2: %v", err)
	}
	var anchor = chainhash.Hash{7, 7, 7}
	pending = make(map[int32]chainhash.Hash)
	filterStart = 2
	anchorPrev = anchor
	var data = []byte{0x0a, 0x0b}
	var raw = filterHash(data)
	pending[2] = raw
	var block2 = hdr2.BlockHash()
	if err := storeFilter(&wire.MsgCFilter{FilterType: wire.GCSFilterRegular, BlockHash: block2, Data: data}); err != nil {
		t.Fatalf("storeFilter at seed height: %v", err)
	}
	var want = filterHeader(raw, anchor)
	var stored, ok, herr = st.FilterHeaderAt(2)
	if herr != nil || !ok || stored != want {
		t.Fatalf("FilterHeaderAt(2) = %s, %v, %v; want %s", stored, ok, herr, want)
	}
	if n, err := st.FilterResumeHeight(2); err != nil || n != 3 {
		t.Fatalf("FilterResumeHeight(2) = %d, %v; want 3", n, err)
	}
}

// TestStoreFilterChainedHeaders builds a two-block chain and verifies that a
// downloaded filter is stored with its chained header and that mismatched
// data is rejected.
func TestStoreFilterChainedHeaders(t *testing.T) {
	resetSync()
	var path = filepath.Join(t.TempDir(), "w.db")
	var st, err = storage.Open(path, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	params = &chaincfg.MainNetParams
	store = st
	chain = NewChain(params)
	if err := chain.Add(&params.GenesisBlock.Header); err != nil {
		t.Fatalf("add genesis: %v", err)
	}
	var ts = params.GenesisBlock.Header.Timestamp.Add(time.Minute)
	var hdr = mineHeader(params.GenesisBlock.Header.BlockHash(), easyBits, ts)
	if err := chain.Add(&hdr); err != nil {
		t.Fatalf("add header 1: %v", err)
	}
	pending = make(map[int32]chainhash.Hash)
	var data0 = []byte{0x0a, 0x0b}
	var raw0 = filterHash(data0)
	var chained0 = filterHeader(raw0, chainhash.Hash{})
	var genHash = params.GenesisBlock.Header.BlockHash()
	if err := st.SaveFilter(storage.Filter{Height: 0, BlockHash: genHash, FilterHeader: chained0, Data: data0}); err != nil {
		t.Fatalf("SaveFilter 0: %v", err)
	}
	var data1 = []byte{0x0c, 0x0d}
	var raw1 = filterHash(data1)
	pending[1] = raw1
	var block1 = hdr.BlockHash()
	if err := storeFilter(&wire.MsgCFilter{FilterType: wire.GCSFilterRegular, BlockHash: block1, Data: data1}); err != nil {
		t.Fatalf("storeFilter: %v", err)
	}
	var stored, ok, herr = st.FilterHeaderAt(1)
	if herr != nil || !ok {
		t.Fatalf("FilterHeaderAt(1) = %s, %v, %v", stored, ok, herr)
	}
	var want1 = filterHeader(raw1, chained0)
	if stored != want1 {
		t.Fatalf("stored chained header = %x, want %x", stored, want1)
	}
	if err := storeFilter(&wire.MsgCFilter{FilterType: wire.GCSFilterRegular, BlockHash: block1, Data: []byte{0xff}}); err == nil {
		t.Fatal("filter with mismatched data accepted")
	}
}
