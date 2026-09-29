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
		{Status{Peers: 1}, 1, "Connected"},
		{Status{Peers: 2, State: StateHeaders, Height: 840000}, 2, "Syncing headers: block 840000"},
		{Status{Peers: 3, State: StateFilters, Height: 12}, 3, "Syncing filters: block 12"},
		{Status{Peers: 5}, 3, "Connected"},
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
