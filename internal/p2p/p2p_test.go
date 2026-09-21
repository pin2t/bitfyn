package p2p

import "testing"

// TestPeerAddrString checks the dialable form of peer addresses.
func TestPeerAddrString(t *testing.T) {
	var v4 = PeerAddr{Host: "192.0.2.1", Port: 8333}
	if v4.String() != "192.0.2.1:8333" {
		t.Fatalf("v4 address = %q, want %q", v4.String(), "192.0.2.1:8333")
	}
	var v6 = PeerAddr{Host: "2001:db8::1", Port: 18333}
	if v6.String() != "[2001:db8::1]:18333" {
		t.Fatalf("v6 address = %q, want %q", v6.String(), "[2001:db8::1]:18333")
	}
}
