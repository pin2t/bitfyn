package spv

import "crypto/sha256"
import "testing"
import "github.com/btcsuite/btcd/btcutil/gcs"
import "github.com/btcsuite/btcd/btcutil/gcs/builder"
import "github.com/btcsuite/btcd/chaincfg/chainhash"

// TestMatchScripts builds a BIP158 basic filter with the reference
// implementation and checks that exactly the contained scripts match.
func TestMatchScripts(t *testing.T) {
	var key = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	var scriptA = []byte{0x00, 0x14, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}
	var scriptB = []byte{0x00, 0x14, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22}
	var filter, err = gcs.BuildGCSFilter(gcsP, gcsM, key, [][]byte{scriptA, scriptB})
	if err != nil {
		t.Fatalf("build filter: %v", err)
	}
	var data, err2 = filter.NBytes()
	if err2 != nil {
		t.Fatalf("serialize filter: %v", err2)
	}
	var blockHash chainhash.Hash
	copy(blockHash[:16], key[:])
	hits, err := matchScripts(data, &blockHash, [][]byte{scriptA, scriptB, {0x51}})
	if err != nil {
		t.Fatalf("matchScripts: %v", err)
	}
	if !hits[0] || !hits[1] || hits[2] {
		t.Fatalf("hits = %v, want {true true false}", hits)
	}
}

// TestFilterHash checks the double SHA256 filter commitment.
func TestFilterHash(t *testing.T) {
	var data = []byte("bitfyn filter test")
	var first = sha256.Sum256(data)
	var want = sha256.Sum256(first[:])
	if got := filterHash(data); got != chainhash.Hash(want) {
		t.Fatalf("filterHash = %x, want %x", got, want)
	}
}

// TestFilterHeader checks the chained filter header definition against the
// btcutil reference implementation, including the null genesis predecessor.
func TestFilterHeader(t *testing.T) {
	var key = [16]byte{9, 9, 9}
	var filter, err = gcs.BuildGCSFilter(gcsP, gcsM, key, [][]byte{{0x00, 0x14}})
	if err != nil {
		t.Fatalf("build filter: %v", err)
	}
	var raw, err2 = builder.GetFilterHash(filter)
	if err2 != nil {
		t.Fatalf("GetFilterHash: %v", err2)
	}
	var prev = chainhash.Hash{1, 2, 3}
	var want, err3 = builder.MakeHeaderForFilter(filter, prev)
	if err3 != nil {
		t.Fatalf("MakeHeaderForFilter: %v", err3)
	}
	if got := filterHeader(raw, prev); got != want {
		t.Fatalf("filterHeader = %x, want %x", got, want)
	}
	var wantZero, err4 = builder.MakeHeaderForFilter(filter, chainhash.Hash{})
	if err4 != nil {
		t.Fatalf("MakeHeaderForFilter zero: %v", err4)
	}
	if got := filterHeader(raw, chainhash.Hash{}); got != wantZero {
		t.Fatalf("genesis filterHeader = %x, want %x", got, wantZero)
	}
}
