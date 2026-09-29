package storage

import "path/filepath"
import "testing"
import "github.com/btcsuite/btcd/chaincfg/chainhash"

// TestSyncTables exercises the SPV tables: headers with rewind deletes,
// filters with header lookups, idempotent matches and the address listing.
func TestSyncTables(t *testing.T) {
	var path = filepath.Join(t.TempDir(), "sync.db")
	var s, err = Open(path, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	var h0 = Header{Height: 0, Hash: chainhash.Hash{1}, MerkleRoot: chainhash.Hash{9}, Version: 1, Timestamp: 100, Bits: 0x1d00ffff}
	var h1 = Header{Height: 1, Hash: chainhash.Hash{2}, PrevHash: h0.Hash, Timestamp: 200, Bits: 0x1d00ffff}
	var h2 = Header{Height: 2, Hash: chainhash.Hash{3}, PrevHash: h1.Hash, Timestamp: 300, Bits: 0x1d00ffff}
	for _, h := range []Header{h0, h1, h2} {
		if err := s.SaveHeader(h); err != nil {
			t.Fatalf("SaveHeader %d: %v", h.Height, err)
		}
	}
	if n, err := s.HeaderCount(); err != nil || n != 3 {
		t.Fatalf("HeaderCount = %d, %v; want 3", n, err)
	}
	got, ok, err := s.HeaderAt(1)
	if err != nil || !ok || got.Hash != h1.Hash || got.PrevHash != h0.Hash {
		t.Fatalf("HeaderAt(1) = %+v, %v, %v", got, ok, err)
	}
	if err := s.DeleteHeadersFrom(0); err != nil {
		t.Fatalf("DeleteHeadersFrom: %v", err)
	}
	if _, ok, _ := s.HeaderAt(2); ok {
		t.Fatal("header 2 still present after rewind")
	}
	var f0 = Filter{Height: 0, BlockHash: chainhash.Hash{4}, FilterHeader: chainhash.Hash{5}, Data: []byte{1, 2, 3}}
	if err := s.SaveFilter(f0); err != nil {
		t.Fatalf("SaveFilter: %v", err)
	}
	header, ok, err := s.FilterHeaderAt(0)
	if err != nil || !ok || header != f0.FilterHeader {
		t.Fatalf("FilterHeaderAt(0) = %s, %v, %v", header, ok, err)
	}
	if n, err := s.FilterCount(); err != nil || n != 1 {
		t.Fatalf("FilterCount = %d, %v; want 1", n, err)
	}
	if err := s.PruneFilterData(0); err != nil {
		t.Fatalf("PruneFilterData: %v", err)
	}
	header, ok, err = s.FilterHeaderAt(0)
	if err != nil || !ok || header != f0.FilterHeader {
		t.Fatalf("FilterHeaderAt(0) after prune = %s, %v, %v", header, ok, err)
	}
	if n, err := s.FilterResumeHeight(0); err != nil || n != 1 {
		t.Fatalf("FilterResumeHeight(0) = %d, %v; want 1", n, err)
	}
	if err := s.SaveFilter(Filter{Height: 1, BlockHash: chainhash.Hash{7}, FilterHeader: chainhash.Hash{8}}); err != nil {
		t.Fatalf("SaveFilter header-only: %v", err)
	}
	if n, err := s.FilterResumeHeight(0); err != nil || n != 2 {
		t.Fatalf("FilterResumeHeight(0) after header-only row = %d, %v; want 2", n, err)
	}
	if err := s.PruneFilterDataFrom(0); err != nil {
		t.Fatalf("PruneFilterDataFrom: %v", err)
	}
	if err := s.DeleteFiltersFrom(0); err != nil {
		t.Fatalf("DeleteFiltersFrom: %v", err)
	}
	if n, err := s.FilterResumeHeight(0); err != nil || n != 1 {
		t.Fatalf("FilterResumeHeight(0) after delete = %d, %v; want 1", n, err)
	}
	var m = Match{Height: 5, BlockHash: chainhash.Hash{6}, Address: "bc1qtest", Script: []byte{0x51}}
	if err := s.SaveMatch(m); err != nil {
		t.Fatalf("SaveMatch: %v", err)
	}
	if err := s.SaveMatch(m); err != nil {
		t.Fatalf("SaveMatch again: %v", err)
	}
	matches, err := s.Matches()
	if err != nil || len(matches) != 1 || matches[0].Address != "bc1qtest" {
		t.Fatalf("Matches = %+v, %v", matches, err)
	}
	if err := s.AddAddress(0, "m/84'/1'/0'/0/0", "tb1qtest", []byte{7, 8, 9}); err != nil {
		t.Fatalf("AddAddress: %v", err)
	}
	addrs, err := s.Addresses()
	if err != nil || len(addrs) != 1 || addrs[0].Pubkey[2] != 9 {
		t.Fatalf("Addresses = %+v, %v", addrs, err)
	}
	if err := s.SavePeer("10.0.0.1", 8333); err != nil {
		t.Fatalf("SavePeer: %v", err)
	}
	if err := s.SavePeer("10.0.0.1", 8333); err != nil {
		t.Fatalf("SavePeer again: %v", err)
	}
	if err := s.UpsertPeer(Peer{Host: "10.0.0.1", Port: 8333, Services: 0x48, LatencyMs: 120}); err != nil {
		t.Fatalf("UpsertPeer: %v", err)
	}
	if err := s.RecordPeerResult("10.0.0.1", 8333, true, 80); err != nil {
		t.Fatalf("RecordPeerResult: %v", err)
	}
	if err := s.RecordPeerResult("10.0.0.1", 8333, true, 90); err != nil {
		t.Fatalf("RecordPeerResult: %v", err)
	}
	if err := s.RecordPeerResult("10.0.0.1", 8333, false, 500); err != nil {
		t.Fatalf("RecordPeerResult: %v", err)
	}
	if err := s.UpdatePeerServices("10.0.0.1", 8333, 0x40040); err != nil {
		t.Fatalf("UpdatePeerServices: %v", err)
	}
	peers, err := s.Peers()
	if err != nil || len(peers) != 1 {
		t.Fatalf("Peers = %+v, %v", peers, err)
	}
	var p = peers[0]
	if p.Host != "10.0.0.1" || p.Port != 8333 || p.Services != 0x40040 || p.LatencyMs != 500 || p.OkCount != 2 || p.FailCount != 1 {
		t.Fatalf("unexpected peer row: %+v", p)
	}
	if err := s.UpdatePeerServices("10.0.0.3", 18444, 0x48); err != nil {
		t.Fatalf("UpdatePeerServices new row: %v", err)
	}
	if n, err := s.Peers(); err != nil || len(n) != 2 || n[1].Services != 0x48 || n[1].OkCount != 0 {
		t.Fatalf("Peers after gossip insert = %+v, %v", n, err)
	}
}

// TestAddPeerColumns checks that a database with the old two-column peers
// table gains the statistics columns without losing its rows.
func TestAddPeerColumns(t *testing.T) {
	var path = filepath.Join(t.TempDir(), "old.db")
	var s, err = Open(path, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	var drops = []string{
		`drop table peers`,
		`create table peers (host text not null, port integer not null, primary key (host, port))`,
		`insert into peers (host, port) values ('10.0.0.2', 18333)`,
	}
	for _, query := range drops {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatalf("%q: %v", query, err)
		}
	}
	if err := addPeerColumns(s.db); err != nil {
		t.Fatalf("addPeerColumns: %v", err)
	}
	if err := s.RecordPeerResult("10.0.0.2", 18333, true, 42); err != nil {
		t.Fatalf("RecordPeerResult on migrated table: %v", err)
	}
	peers, err := s.Peers()
	if err != nil || len(peers) != 1 || peers[0].OkCount != 1 || peers[0].LatencyMs != 42 {
		t.Fatalf("Peers after migration = %+v, %v", peers, err)
	}
}

// TestUpgradeSchema checks that filters stored under the old scheme are
// cleared once, the cfilters table is rebuilt with a nullable filterData
// column, and the schema version is stamped.
func TestUpgradeSchema(t *testing.T) {
	var path = filepath.Join(t.TempDir(), "f.db")
	var s, err = Open(path, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	var inserts = []string{
		`insert into cfilters (height, blockHash, filterHeader, filterData) values (7, x'11', x'22', x'33')`,
		`pragma user_version = 0`,
	}
	for _, query := range inserts {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatalf("%q: %v", query, err)
		}
	}
	if err := upgradeSchema(s.db); err != nil {
		t.Fatalf("upgradeSchema: %v", err)
	}
	if n, err := s.FilterCount(); err != nil || n != 0 {
		t.Fatalf("FilterCount after upgrade = %d, %v; want 0", n, err)
	}
	var version int
	if err := s.db.QueryRow(`pragma user_version`).Scan(&version); err != nil || version != filterPruneVersion {
		t.Fatalf("user_version = %d, %v; want %d", version, err, filterPruneVersion)
	}
	if err := upgradeSchema(s.db); err != nil {
		t.Fatalf("upgradeSchema again: %v", err)
	}
	if err := s.SaveFilter(Filter{Height: 9, BlockHash: chainhash.Hash{1}, FilterHeader: chainhash.Hash{2}}); err != nil {
		t.Fatalf("SaveFilter with nil data after rebuild: %v", err)
	}
	if err := s.PruneFilterData(9); err != nil {
		t.Fatalf("PruneFilterData after rebuild: %v", err)
	}
	if n, err := s.FilterResumeHeight(0); err != nil || n != 10 {
		t.Fatalf("FilterResumeHeight after header-only row = %d, %v; want 10", n, err)
	}
}

// TestTransactions checks that wallet transactions round-trip in block order
// and that a rewind drops the transactions and matches above the fork.
func TestTransactions(t *testing.T) {
	var st, err = Open(filepath.Join(t.TempDir(), "w.db"), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	var first = Transaction{Txid: chainhash.Hash{1}, Height: 7, BlockHash: chainhash.Hash{7}, Raw: []byte{1, 2}}
	var second = Transaction{Txid: chainhash.Hash{2}, Height: 9, BlockHash: chainhash.Hash{9}, Raw: []byte{3}}
	for _, tx := range []Transaction{second, first} {
		if err := st.SaveTransaction(tx); err != nil {
			t.Fatalf("SaveTransaction: %v", err)
		}
	}
	var got, gerr = st.Transactions()
	if gerr != nil || len(got) != 2 || got[0].Txid != first.Txid || got[1].BlockHash != second.BlockHash || string(got[0].Raw) != string(first.Raw) {
		t.Fatalf("Transactions = %+v, %v", got, gerr)
	}
	if err := st.SaveMatch(Match{Height: 9, BlockHash: chainhash.Hash{9}, Address: "a", Script: []byte{1}}); err != nil {
		t.Fatalf("SaveMatch: %v", err)
	}
	if err := st.DeleteTransactionsFrom(8); err != nil {
		t.Fatalf("DeleteTransactionsFrom: %v", err)
	}
	if err := st.DeleteMatchesFrom(8); err != nil {
		t.Fatalf("DeleteMatchesFrom: %v", err)
	}
	got, gerr = st.Transactions()
	if gerr != nil || len(got) != 1 || got[0].Txid != first.Txid {
		t.Fatalf("Transactions after rewind = %+v, %v", got, gerr)
	}
	if matches, err := st.Matches(); err != nil || len(matches) != 0 {
		t.Fatalf("Matches after rewind = %+v, %v", matches, err)
	}
}

// TestBatchWrites checks that header and filter batches are written in one
// go and that headers stream back in height order.
func TestBatchWrites(t *testing.T) {
	var st, err = Open(filepath.Join(t.TempDir(), "w.db"), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	var headers = []Header{
		{Height: 1, Hash: chainhash.Hash{1}, PrevHash: chainhash.Hash{0}, Timestamp: 11, Bits: 5, Nonce: 6},
		{Height: 0, Hash: chainhash.Hash{0}, Timestamp: 10},
		{Height: 2, Hash: chainhash.Hash{2}, PrevHash: chainhash.Hash{1}, MerkleRoot: chainhash.Hash{9}, Version: 4},
	}
	if err := st.SaveHeaders(headers); err != nil {
		t.Fatalf("SaveHeaders: %v", err)
	}
	var got []Header
	if err := st.Headers(func(h Header) error {
		got = append(got, h)
		return nil
	}); err != nil {
		t.Fatalf("Headers: %v", err)
	}
	if len(got) != 3 || got[0] != headers[1] || got[1] != headers[0] || got[2] != headers[2] {
		t.Fatalf("Headers = %+v", got)
	}
	if err := st.SaveFilters([]Filter{
		{Height: 5, BlockHash: chainhash.Hash{5}, FilterHeader: chainhash.Hash{50}},
		{Height: 6, BlockHash: chainhash.Hash{6}, FilterHeader: chainhash.Hash{60}},
	}); err != nil {
		t.Fatalf("SaveFilters: %v", err)
	}
	if h, ok, err := st.FilterHeaderAt(6); err != nil || !ok || h != (chainhash.Hash{60}) {
		t.Fatalf("FilterHeaderAt(6) = %s, %v, %v", h, ok, err)
	}
	if n, err := st.FilterResumeHeight(5); err != nil || n != 7 {
		t.Fatalf("FilterResumeHeight(5) = %d, %v; want 7", n, err)
	}
}

// TestPending checks that unconfirmed transactions keep their first-seen
// time, can be deleted one by one and expire by age.
func TestPending(t *testing.T) {
	var st, err = Open(filepath.Join(t.TempDir(), "w.db"), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	var old = PendingTx{Txid: chainhash.Hash{1}, Raw: []byte{1}, SeenAt: 100}
	var fresh = PendingTx{Txid: chainhash.Hash{2}, Raw: []byte{2}, SeenAt: 200}
	for _, p := range []PendingTx{fresh, old, {Txid: chainhash.Hash{1}, Raw: []byte{1}, SeenAt: 300}} {
		if err := st.SavePending(p); err != nil {
			t.Fatalf("SavePending: %v", err)
		}
	}
	var got, gerr = st.PendingTransactions()
	if gerr != nil || len(got) != 2 || got[0].Txid != old.Txid || got[0].SeenAt != 100 || got[1].Txid != fresh.Txid {
		t.Fatalf("PendingTransactions = %+v, %v", got, gerr)
	}
	if n, err := st.DeletePendingBefore(150); err != nil || n != 1 {
		t.Fatalf("DeletePendingBefore = %d, %v; want 1", n, err)
	}
	if err := st.DeletePending(fresh.Txid); err != nil {
		t.Fatalf("DeletePending: %v", err)
	}
	if got, err := st.PendingTransactions(); err != nil || len(got) != 0 {
		t.Fatalf("PendingTransactions after deletes = %+v, %v", got, err)
	}
}
