package sync

import "errors"
import "testing"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/peer"

// testConn is an unconnected peer usable for scheduling tests.
func testConn(t *testing.T, addr string, pinned bool) *conn {
	t.Helper()
	var p, err = peer.NewOutboundPeer(&peer.Config{ChainParams: &chaincfg.RegressionNetParams}, addr)
	if err != nil {
		t.Fatalf("NewOutboundPeer: %v", err)
	}
	var c, cerr = newConn(addr)
	if cerr != nil {
		t.Fatalf("newConn: %v", cerr)
	}
	c.peer = p
	c.pinned = pinned
	return c
}

// TestFanOutStraggler checks that a peer answering long after the primary
// is dropped, while answers within the grace are kept in peer order.
func TestFanOutStraggler(t *testing.T) {
	var saved = stragglerGrace
	stragglerGrace = 100 * time.Millisecond
	defer func() { stragglerGrace = saved }()
	var primaryPeer = testConn(t, "10.0.0.1:8333", false)
	var quick = testConn(t, "10.0.0.2:8333", false)
	var slow = testConn(t, "10.0.0.3:8333", false)
	var delays = map[*conn]time.Duration{primaryPeer: 30 * time.Millisecond, quick: 5 * time.Millisecond, slow: 5 * time.Second}
	var started = time.Now()
	var peers, results = fanOut([]*conn{primaryPeer, quick, slow}, "test", func(c *conn) (string, error) {
		time.Sleep(delays[c])
		return c.addr, nil
	})
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("fanOut waited %s for the straggler", elapsed)
	}
	if len(peers) != 2 || peers[0] != primaryPeer || peers[1] != quick || results[1] != quick.addr {
		t.Fatalf("answered peers = %v, results %v", peers, results)
	}
}

// TestFanOutPinnedFailure checks that a failing pinned primary is kept
// connected, the others still answer, and requirePrimary reports it.
func TestFanOutPinnedFailure(t *testing.T) {
	var pinnedPeer = testConn(t, "10.0.0.1:8333", true)
	var other = testConn(t, "10.0.0.2:8333", false)
	var failing = testConn(t, "10.0.0.3:8333", false)
	var peers, _ = fanOut([]*conn{pinnedPeer, other, failing}, "test", func(c *conn) (int, error) {
		if c == other { return 1, nil }
		return 0, errors.New("boom")
	})
	if len(peers) != 1 || peers[0] != other {
		t.Fatalf("answered peers = %v", peers)
	}
	var saved, savedSeen = pinnedAddr, pinnedSeen
	pinnedAddr = pinnedPeer.addr
	pinnedSeen = time.Now()
	defer func() { pinnedAddr, pinnedSeen = saved, savedSeen }()
	if err := requirePrimary(peers); err == nil {
		t.Fatal("missing pinned peer not reported")
	}
	pinnedSeen = time.Now().Add(-pinnedFallbackAfter)
	if err := requirePrimary(peers); err != nil {
		t.Fatalf("pinned peer away long enough but required: %v", err)
	}
	pinnedSeen = time.Now()
	if err := requirePrimary([]*conn{pinnedPeer, other}); err != nil {
		t.Fatalf("pinned peer present but reported: %v", err)
	}
}

// TestPinnedOrdering checks that the pinned peer leads the peer order and is
// the only primary allowed while it is set, until it has been away for
// pinnedFallbackAfter.
func TestPinnedOrdering(t *testing.T) {
	var a = testConn(t, "10.0.0.1:8333", false)
	var b = testConn(t, "10.0.0.2:8333", false)
	var pin = testConn(t, "10.0.0.3:8333", true)
	var savedPool = pool
	var savedPinned = pinnedAddr
	var savedSeen = pinnedSeen
	defer func() {
		pool = savedPool
		pinnedAddr = savedPinned
		pinnedSeen = savedSeen
	}()
	pool = []*conn{a, pin, b}
	pinnedAddr = pin.addr
	pinnedSeen = time.Now()
	if got := orderedPeers(); len(got) != 3 || got[0] != pin || got[1] != a || got[2] != b {
		t.Fatalf("orderedPeers = %v", got)
	}
	if primary() != pin {
		t.Fatal("pinned peer is not the primary")
	}
	pool = []*conn{a, b}
	if primary() != nil || len(filterPeers()) != 0 {
		t.Fatal("sync may run without the pinned peer")
	}
	if fallbackTimer() == nil {
		t.Fatal("no timer for the fallback")
	}
	pinnedSeen = time.Now().Add(-pinnedFallbackAfter)
	if primary() != a || len(filterPeers()) != 2 || fallbackTimer() != nil {
		t.Fatal("sync does not fall back to the oldest peer once the pinned peer is away")
	}
	pool = []*conn{a, pin, b}
	if primary() != pin {
		t.Fatal("pinned peer is not the primary again once back")
	}
	pool = []*conn{a, b}
	pinnedAddr = ""
	if primary() != a {
		t.Fatal("oldest peer is not the primary without pinning")
	}
}
