package sync

import "bytes"
import "testing"
import "time"
import "github.com/btcsuite/btcd/btcutil/gcs/builder"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/spv"
import "bitfyn/internal/storage"

// testChain gives the sync a regtest header chain of the given height.
func testChain(t *testing.T, height int) {
	t.Helper()
	var genesis = chaincfg.RegressionNetParams.GenesisBlock.Header
	chain = spv.NewChain(&chaincfg.RegressionNetParams)
	if err := chain.Add(&genesis); err != nil {
		t.Fatalf("add genesis: %v", err)
	}
	for h := 1; h <= height; h++ {
		var tip, _ = chain.Tip()
		var hdr = mineHeader(tip.Hash, easyBits, genesis.Timestamp.Add(time.Duration(h)*10*time.Minute))
		if err := chain.AppendTrusted(&hdr); err != nil {
			t.Fatalf("append header %d: %v", h, err)
		}
	}
}

// chainFilter builds the basic filter of the block at the height of the
// test chain over the entries.
func chainFilter(t *testing.T, height int32, entries ...[]byte) []byte {
	t.Helper()
	var hdr, _ = chain.HeaderAt(height)
	var filter, err = builder.WithKeyHash(&hdr.Hash).AddEntries(entries).Build()
	if err != nil {
		t.Fatalf("build filter: %v", err)
	}
	data, err := filter.NBytes()
	if err != nil {
		t.Fatalf("serialize filter: %v", err)
	}
	return data
}

// saveTx stores a confirmed wallet transaction directly, as a block scanned
// earlier did.
func saveTx(t *testing.T, height int32, tx *wire.MsgTx) {
	t.Helper()
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if err := store.SaveTransaction(storage.Transaction{Txid: tx.TxHash(), Height: height, Raw: buf.Bytes()}); err != nil {
		t.Fatalf("SaveTransaction: %v", err)
	}
}

// TestWatchPaidAddress reproduces a change address another wallet on the
// same seed used first: a stored transaction spending a wallet coin also
// paid it. Watching it shows the coin at once, marks it used and queues a
// rescan from the block that paid it.
func TestWatchPaidAddress(t *testing.T) {
	var mine = testWallet(t)
	testChain(t, 10)
	var pubkey = bytes.Repeat([]byte{3}, 33)
	var late = p2wpkhScript(pubkey)
	var receive = testTx(wire.OutPoint{Hash: chainhash.Hash{1}}, mine)
	var consolidate = testTx(wire.OutPoint{Hash: receive.TxHash()}, []byte{0x51}, late)
	saveTx(t, 2, receive)
	saveTx(t, 4, consolidate)
	if err := loadWallet(); err != nil {
		t.Fatalf("loadWallet: %v", err)
	}
	if conf, _ := walletBalance(); conf != 0 {
		t.Fatalf("balance before watching = %d, want 0", conf)
	}
	Watch("bc1late", "m/84'/1'/0'/1/1", pubkey)
	if conf, _ := walletBalance(); conf != 2000 {
		t.Fatalf("balance after watching = %d, want 2000", conf)
	}
	if !IsUsed("bc1late") {
		t.Fatal("address paid before it was watched not reported used")
	}
	var jobs, err = store.Rescans()
	if err != nil || len(jobs) != 1 || jobs[0] != (storage.Rescan{Address: "bc1late", From: 4}) {
		t.Fatalf("Rescans = %+v, %v; want bc1late from 4", jobs, err)
	}
	Watch("bc1fresh", "m/84'/1'/0'/1/2", bytes.Repeat([]byte{4}, 33))
	if jobs, _ := store.Rescans(); len(jobs) != 1 {
		t.Fatalf("an unpaid address queued a rescan: %+v", jobs)
	}
}

// TestRescanTargets checks that a rescan starts at the first block paying
// the address, not below its queued height or the filter start, and that
// unwatched, unpaid or already scanned addresses have nothing to rescan.
func TestRescanTargets(t *testing.T) {
	var mine = testWallet(t)
	testChain(t, 10)
	saveTx(t, 3, testTx(wire.OutPoint{Hash: chainhash.Hash{1}}, mine))
	saveTx(t, 6, testTx(wire.OutPoint{Hash: chainhash.Hash{2}}, mine))
	var other = bytes.Repeat([]byte{5}, 33)
	Watch("bc1other", "m/84'/1'/0'/0/1", other)
	if err := loadWallet(); err != nil {
		t.Fatalf("loadWallet: %v", err)
	}
	var check = func(job storage.Rescan, end int32, want int32) {
		t.Helper()
		var targets, idle = rescanTargets([]storage.Rescan{job}, end)
		if want < 0 {
			if len(targets) != 0 || len(idle) != 1 {
				t.Fatalf("%+v up to %d: targets %+v, idle %+v; want idle", job, end, targets, idle)
			}
			return
		}
		if len(targets) != 1 || targets[0].from != want || targets[0].script.address != job.Address {
			t.Fatalf("%+v up to %d: targets %+v, want from %d", job, end, targets, want)
		}
	}
	check(storage.Rescan{Address: "bc1mine", From: 0}, 10, 3)
	check(storage.Rescan{Address: "bc1mine", From: 5}, 10, 5)
	filterStart = 4
	check(storage.Rescan{Address: "bc1mine", From: 0}, 10, 4)
	check(storage.Rescan{Address: "bc1mine", From: 0}, 3, -1)
	check(storage.Rescan{Address: "bc1other", From: 0}, 10, -1)
	check(storage.Rescan{Address: "bc1gone", From: 0}, 10, -1)
}

// TestRescanFindsMissedSpend checks that rescanning a block whose filter
// matches a late-watched address stores the transaction spending its coin,
// which the first scan missed, and records the match; blocks below the
// target start and blocks matched for the address before are not fetched.
func TestRescanFindsMissedSpend(t *testing.T) {
	var mine = testWallet(t)
	testChain(t, 10)
	var pubkey = bytes.Repeat([]byte{3}, 33)
	var late = p2wpkhScript(pubkey)
	var receive = testTx(wire.OutPoint{Hash: chainhash.Hash{1}}, mine)
	var consolidate = testTx(wire.OutPoint{Hash: receive.TxHash()}, []byte{0x51}, late)
	var spend = testTx(wire.OutPoint{Hash: consolidate.TxHash(), Index: 1}, []byte{0x52})
	saveTx(t, 2, receive)
	saveTx(t, 4, consolidate)
	Watch("bc1late", "m/84'/1'/0'/1/1", pubkey)
	if conf, _ := walletBalance(); conf != 2000 {
		t.Fatalf("balance after watching = %d, want 2000", conf)
	}
	var jobs, _ = store.Rescans()
	var targets, _ = rescanTargets(jobs, 10)
	var matched = map[matchKey]bool{{8, "bc1late"}: true}
	var fetched []int32
	var getBlock = func(height int32) (*wire.MsgBlock, error) {
		fetched = append(fetched, height)
		return testBlock(spend), nil
	}
	for _, height := range []int32{3, 8, 6} {
		if err := rescanBlock(height, chainFilter(t, height, late), targets, matched, getBlock); err != nil {
			t.Fatalf("rescanBlock %d: %v", height, err)
		}
	}
	if err := rescanBlock(7, chainFilter(t, 7, []byte{0x53}), targets, matched, getBlock); err != nil {
		t.Fatalf("rescanBlock 7: %v", err)
	}
	if len(fetched) != 1 || fetched[0] != 6 {
		t.Fatalf("fetched blocks %v, want only 6", fetched)
	}
	if conf, _ := walletBalance(); conf != 0 {
		t.Fatalf("balance after the rescan = %d, want 0", conf)
	}
	if list := Coins(); len(list) != 0 {
		t.Fatalf("coins after the rescan = %+v, want none", list)
	}
	var stored, _ = store.Matches()
	if len(stored) != 1 || stored[0].Height != 6 || stored[0].Address != "bc1late" {
		t.Fatalf("matches = %+v, want bc1late at 6", stored)
	}
}

// TestCheckStoredChain checks that refetched filters are accepted only when
// they chain to the stored filter headers.
func TestCheckStoredChain(t *testing.T) {
	testWallet(t)
	testChain(t, 3)
	var anchor = chainhash.Hash{9}
	var filters = [][]byte{{1}, {2}, {3}}
	var prev = anchor
	for i, data := range filters {
		var hdr, _ = chain.HeaderAt(int32(i + 1))
		prev = spv.FilterHeader(spv.FilterHash(data), prev)
		if err := store.SaveFilter(storage.Filter{Height: int32(i + 1), BlockHash: hdr.Hash, FilterHeader: prev}); err != nil {
			t.Fatalf("SaveFilter: %v", err)
		}
	}
	if err := checkStoredChain(1, anchor, filters); err != nil {
		t.Fatalf("matching filters rejected: %v", err)
	}
	if err := checkStoredChain(1, chainhash.Hash{8}, filters); err == nil {
		t.Fatal("filters from a wrong previous header accepted")
	}
	if err := checkStoredChain(1, anchor, [][]byte{{1}, {7}, {3}}); err == nil {
		t.Fatal("a forged filter accepted")
	}
	if err := checkStoredChain(4, prev, [][]byte{{4}}); err == nil {
		t.Fatal("filters past the stored headers accepted")
	}
}
