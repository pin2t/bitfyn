package p2p

import "testing"
import "time"

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

// TestKeepAlive checks that an unanswered connection is given up KeepAlive
// after it went silent: the idle time plus every probe interval.
func TestKeepAlive(t *testing.T) {
	var cfg = keepAliveConfig()
	if !cfg.Enable || cfg.Count != keepAliveProbes {
		t.Fatalf("keepalive %+v not enabled with %d probes", cfg, keepAliveProbes)
	}
	if total := cfg.Idle + time.Duration(cfg.Count)*cfg.Interval; total != KeepAlive {
		t.Fatalf("keepalive gives up after %s, want %s", total, KeepAlive)
	}
}
