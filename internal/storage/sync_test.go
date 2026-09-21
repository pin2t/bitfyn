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
	peers, err := s.Peers()
	if err != nil || len(peers) != 1 {
		t.Fatalf("Peers = %+v, %v", peers, err)
	}
	var p = peers[0]
	if p.Host != "10.0.0.1" || p.Port != 8333 || p.Services != 0x48 || p.LatencyMs != 500 || p.OkCount != 2 || p.FailCount != 1 {
		t.Fatalf("unexpected peer row: %+v", p)
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
