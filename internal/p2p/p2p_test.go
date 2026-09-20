package p2p

import "testing"
import "github.com/btcsuite/btcd/chaincfg"

// TestResolveAddressExplicit checks that an explicit host:port is kept.
func TestResolveAddressExplicit(t *testing.T) {
	var got, err = ResolveAddress(&chaincfg.MainNetParams, "127.0.0.1:8333")
	if err != nil {
		t.Fatalf("ResolveAddress: %v", err)
	}
	if got != "127.0.0.1:8333" {
		t.Fatalf("address = %q, want %q", got, "127.0.0.1:8333")
	}
}

// TestResolveAddressNeedsPort checks that a port-less address is rejected.
func TestResolveAddressNeedsPort(t *testing.T) {
	if _, err := ResolveAddress(&chaincfg.MainNetParams, "127.0.0.1"); err == nil {
		t.Fatal("address without port accepted")
	}
}

// TestResolveAddressRegtest checks the local fallback for regtest.
func TestResolveAddressRegtest(t *testing.T) {
	var got, err = ResolveAddress(&chaincfg.RegressionNetParams, "")
	if err != nil {
		t.Fatalf("ResolveAddress: %v", err)
	}
	if got != "127.0.0.1:18444" {
		t.Fatalf("address = %q, want %q", got, "127.0.0.1:18444")
	}
}
