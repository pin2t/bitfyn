package wallet

import "strings"
import "testing"
import "github.com/btcsuite/btcd/chaincfg"

// vectorMnemonic is the well-known BIP84 test vector mnemonic (mainnet).
const vectorMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

// TestBIP84Vector checks the official BIP84 test vector: the first receive
// address derived from the vector mnemonic.
func TestBIP84Vector(t *testing.T) {
	var w, err = New(vectorMnemonic, "", &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	addr, path, pub, err := w.DeriveAddress(0)
	if err != nil {
		t.Fatalf("DeriveAddress: %v", err)
	}
	const wantAddr = "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu"
	const wantPath = "m/84'/0'/0'/0/0"
	if addr != wantAddr {
		t.Errorf("address = %s, want %s", addr, wantAddr)
	}
	if path != wantPath {
		t.Errorf("path = %s, want %s", path, wantPath)
	}
	if len(pub) != 33 {
		t.Errorf("pubkey length = %d, want 33 (compressed)", len(pub))
	}
}

// TestDeterministicDerivation checks that restoring the same mnemonic yields
// the same addresses and that distinct indices yield distinct addresses.
func TestDeterministicDerivation(t *testing.T) {
	var w1, err = New(vectorMnemonic, "", &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w2, err := New(vectorMnemonic, "", &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var prev string
	for i := uint32(0); i < 5; i++ {
		var a1, _, _, err = w1.DeriveAddress(i)
		if err != nil {
			t.Fatalf("DeriveAddress(%d): %v", i, err)
		}
		a2, _, _, err := w2.DeriveAddress(i)
		if err != nil {
			t.Fatalf("DeriveAddress(%d) second wallet: %v", i, err)
		}
		if a1 != a2 {
			t.Errorf("index %d: address %s != %s across restores", i, a1, a2)
		}
		if a1 == prev {
			t.Errorf("index %d: address equals previous index", i)
		}
		prev = a1
	}
}

// TestTestnet checks the testnet coin type (1') and the tb1 address prefix.
func TestTestnet(t *testing.T) {
	var mnemonic, err = NewMnemonic(128)
	if err != nil {
		t.Fatalf("NewMnemonic: %v", err)
	}
	w, err := New(mnemonic, "", &chaincfg.TestNet3Params)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	addr, path, _, err := w.DeriveAddress(0)
	if err != nil {
		t.Fatalf("DeriveAddress: %v", err)
	}
	if !strings.HasPrefix(addr, "tb1") {
		t.Errorf("testnet address = %s, want tb1 prefix", addr)
	}
	if want := "m/84'/1'/0'/0/0"; path != want {
		t.Errorf("testnet path = %s, want %s", path, want)
	}
}

// TestAccountXPub checks the account-level key against the BIP84 test vector
// zpub for m/84'/0'/0'.
func TestAccountXPub(t *testing.T) {
	var w, err = New(vectorMnemonic, "", &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	xpub, err := w.AccountXPub()
	if err != nil {
		t.Fatalf("AccountXPub: %v", err)
	}
	const want = "zpub6rFR7y4Q2AijBEqTUquhVz398htDFrtymD9xYYfG1m4wAcvPhXNfE3EfH1r1ADqtfSdVCToUG868RvUUkgDKf31mGDtKsAYz2oz2AGutZYs"
	if xpub != want {
		t.Errorf("xpub = %s, want %s", xpub, want)
	}
}

// TestParamsForNetwork checks network name resolution.
func TestParamsForNetwork(t *testing.T) {
	var cases = map[string]string{
		"mainnet": "mainnet",
		"bitcoin": "mainnet",
		"testnet": "testnet3",
		"regtest": "regtest",
		"simnet":  "simnet",
	}
	for in, want := range cases {
		var p, err = ParamsForNetwork(in)
		if err != nil {
			t.Fatalf("ParamsForNetwork(%q): %v", in, err)
		}
		if p.Name != want {
			t.Errorf("ParamsForNetwork(%q) = %s, want %s", in, p.Name, want)
		}
	}
	if _, err := ParamsForNetwork("bogus"); err == nil {
		t.Error("ParamsForNetwork on bogus input should fail")
	}
}
