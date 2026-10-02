package sync

import "path/filepath"
import "testing"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "bitfyn/internal/storage"

// TestStatus checks the bar count and the status line for every state.
func TestStatus(t *testing.T) {
	var cases = []struct {
		status Status
		bars   int
		text   string
	}{
		{Status{}, 0, "Not connected"},
		{Status{State: StateHeaders, Height: 5}, 0, "Not connected"},
		{Status{Peers: 1, Synced: true}, 1, "Connected"},
		{Status{Peers: 1, Height: 969463}, 1, "Syncing (969463)..."},
		{Status{Peers: 2, State: StateHeaders, Height: 840000}, 2, "Syncing (840000)..."},
		{Status{Peers: 3, State: StateFilters, Height: 12}, 3, "Syncing (12)..."},
		{Status{Peers: 5, Synced: true}, 3, "Connected"},
	}
	for _, c := range cases {
		if got := c.status.Bars(); got != c.bars {
			t.Errorf("%+v: Bars = %d, want %d", c.status, got, c.bars)
		}
		if got := c.status.String(); got != c.text {
			t.Errorf("%+v: String = %q, want %q", c.status, got, c.text)
		}
	}
}

// TestStartStopUnreachable checks that the manager reports "Not connected"
// while no peer is reachable and shuts down promptly.
func TestStartStopUnreachable(t *testing.T) {
	resetSync()
	var st, err = storage.Open(filepath.Join(t.TempDir(), "w.db"), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	var last = make(chan Status, 16)
	err = Start(&chaincfg.RegressionNetParams, st, "127.0.0.1:1", func(s Status) {
		select {
		case last <- s:
		default:
		}
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var first = <-last
	if first.Peers != 0 || first.String() != "Not connected" {
		t.Fatalf("initial status = %+v (%q)", first, first.String())
	}
	var stopped = make(chan struct{})
	go func() {
		Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return")
	}
	Stop()
}

// TestStartRejectsPeerWithoutPort checks the explicit peer validation.
func TestStartRejectsPeerWithoutPort(t *testing.T) {
	if err := Start(&chaincfg.MainNetParams, nil, "example.org", nil); err == nil {
		t.Fatal("peer address without a port accepted")
	}
}

// TestSynced checks that the wallet counts as synced only while idle after a
// successful round, with no unknown block announced since and no peer
// advertising a higher tip.
func TestSynced(t *testing.T) {
	var a = testConn(t, "10.0.0.1:8333", false)
	var b = testConn(t, "10.0.0.2:8333", false)
	a.peer.UpdateLastBlockHeight(100)
	b.peer.UpdateLastBlockHeight(100)
	mu.Lock()
	defer mu.Unlock()
	var savedPool, savedActivity, savedHeight = pool, activity, height
	var savedOK, savedAnnounced, savedThrough = roundOK, announced, syncedThrough
	defer func() {
		pool, activity, height = savedPool, savedActivity, savedHeight
		roundOK, announced, syncedThrough = savedOK, savedAnnounced, savedThrough
	}()
	pool, activity, height, roundOK, announced, syncedThrough = []*conn{a, b}, StateIdle, 100, true, 2, 2
	if !syncedLocked() {
		t.Fatal("idle at the peers' tip after a good round is not synced")
	}
	activity = StateHeaders
	if syncedLocked() {
		t.Fatal("synced while syncing headers")
	}
	activity = StateIdle
	roundOK = false
	if syncedLocked() {
		t.Fatal("synced after a failed round")
	}
	roundOK = true
	announced = 3
	if syncedLocked() {
		t.Fatal("synced with a new block announced")
	}
	announced = 2
	b.peer.UpdateLastBlockHeight(101)
	if syncedLocked() {
		t.Fatal("synced below a peer's advertised tip")
	}
}
