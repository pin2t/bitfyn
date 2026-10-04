package gui

import "context"
import "fmt"
import "syscall"
import "testing"
import "time"
import "fyne.io/fyne/v2/test"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "bitfyn/internal/p2p"
import "bitfyn/internal/storage"

// TestParsePeer checks the accepted forms of a typed peer: an IP address
// with or without a port, IPv6 in brackets when it has one.
func TestParsePeer(t *testing.T) {
	var cases = []struct {
		text string
		want string
	}{
		{"192.0.2.1", "192.0.2.1:8333"},
		{"192.0.2.1:18444", "192.0.2.1:18444"},
		{"192.0.2.1:08333", "192.0.2.1:8333"},
		{"2001:db8::1", "[2001:db8::1]:8333"},
		{"[2001:db8::1]", "[2001:db8::1]:8333"},
		{"[2001:db8::1]:8334", "[2001:db8::1]:8334"},
		{"192.0.2", ""},
		{"192.0.2.1:", ""},
		{"192.0.2.1:0", ""},
		{"192.0.2.1:65536", ""},
		{"example.com:8333", ""},
		{"[2001:db8::1", ""},
	}
	for _, c := range cases {
		var got, ok = parsePeer(c.text, "8333")
		if ok != (c.want != "") || got != c.want {
			t.Errorf("parsePeer(%q) = %q, %v; want %q", c.text, got, ok, c.want)
		}
	}
}

// TestProbeReason checks the short reasons shown for a failed connection.
func TestProbeReason(t *testing.T) {
	var cases = []struct {
		err  error
		want string
	}{
		{fmt.Errorf("192.0.2.1:8333: %w", p2p.ErrNoCompactFilters), "no compact filters (needs blockfilterindex=1 and peerblockfilters=1)"},
		{fmt.Errorf("dial peer: %w", syscall.ECONNREFUSED), "connection refused"},
		{fmt.Errorf("dial peer: %w", context.DeadlineExceeded), "no answer"},
		{fmt.Errorf("handshake with 192.0.2.1:8333 timed out"), "no handshake"},
	}
	for _, c := range cases {
		if got := probeReason(c.err); got != c.want {
			t.Errorf("probeReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// TestSettingsPinnedPeer types peers into the pinned peer field: one that
// connects is shown in green, saved and pinned at once, with the network's
// default port when none is typed; one that does not connect, or is not an
// IP address, is reported and leaves the pinned peer alone; an empty field
// unpins it.
func TestSettingsPinnedPeer(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var w = test.NewWindow(nil)
	defer w.Close()
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil { t.Fatalf("newGUI: %v", err) }
	defer g.store.Close()
	var v, checked, applied = testSettings(g)
	var pinned = func(want string) {
		t.Helper()
		var got, err = g.store.Setting(storage.SettingPinnedPeer)
		if err != nil || got != want {
			t.Fatalf("saved pinned peer %q, %v; want %q", got, err, want)
		}
		if last := (*applied)[len(*applied)-1]; last != want {
			t.Fatalf("sync pinned to %q, want %q", last, want)
		}
	}
	var typeIn = func(text, status string) {
		t.Helper()
		v.peer.SetText(text)
		select {
		case <-checked:
		case <-time.After(5 * time.Second):
			t.Fatalf("no check of %q", text)
		}
		if got := v.statusText(); got != status {
			t.Fatalf("status after %q is %q, want %q", text, got, status)
		}
	}
	typeIn("192.0.2.1", "Connected")
	pinned("192.0.2.1:18444")
	if c := v.status.Segments[0].(*widget.TextSegment).Style.ColorName; c != theme.ColorNameSuccess {
		t.Fatalf("connected status in %q, want green %q", c, theme.ColorNameSuccess)
	}
	typeIn("192.0.2.2", "Not connected: connection refused")
	pinned("192.0.2.1:18444")
	v.peer.SetText("192.0.2")
	if got := v.statusText(); got != "Not connected: not an IP address or IP:port" {
		t.Fatalf("status of a partial address is %q", got)
	}
	pinned("192.0.2.1:18444")
	typeIn("192.0.2.1:18444", "Connected")
	if len(*applied) != 1 {
		t.Fatalf("peer in use pinned again: %v", *applied)
	}
	v.peer.SetText("")
	if got := v.statusText(); got != "" {
		t.Fatalf("status of an empty field is %q, want none", got)
	}
	pinned("")
}

// TestSettingsFlagPeer checks that the field shows the peer given with the
// -peer flag, and that checking it again neither saves nor pins it anew.
func TestSettingsFlagPeer(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var w = test.NewWindow(nil)
	defer w.Close()
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest", Peer: "192.0.2.1:18444"}, w)
	if err != nil { t.Fatalf("newGUI: %v", err) }
	defer g.store.Close()
	var v, checked, applied = testSettings(g)
	if v.peer.Text != "192.0.2.1:18444" {
		t.Fatalf("field shows %q, want the -peer flag", v.peer.Text)
	}
	v.recheck()
	<-checked
	if v.statusText() != "Connected" || len(*applied) != 0 {
		t.Fatalf("recheck: status %q, pinned %v; want connected and nothing pinned", v.statusText(), *applied)
	}
	if saved, _ := g.store.Setting(storage.SettingPinnedPeer); saved != "" {
		t.Fatalf("the -peer flag was saved as %q", saved)
	}
}

// testSettings builds the settings view of g checking peers at once: only
// 192.0.2.1:18444 connects. It returns the view, a channel told of every
// finished check, and the peers the sync was pinned to.
func testSettings(g *gui) (*settingsView, chan struct{}, *[]string) {
	var v = newSettingsView(g)
	var checked = make(chan struct{}, 1)
	var applied []string
	v.delay = 0
	v.checked = func() { checked <- struct{}{} }
	v.probe = func(_ context.Context, addr string) error {
		if addr == "192.0.2.1:18444" { return nil }
		return fmt.Errorf("dial peer %s: %w", addr, syscall.ECONNREFUSED)
	}
	v.apply = func(addr string) error {
		applied = append(applied, addr)
		return nil
	}
	return v, checked, &applied
}

// TestPinnedPeerFlagWins checks that the -peer flag wins over the pinned
// peer saved in the settings, which is used without the flag.
func TestPinnedPeerFlagWins(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var dir = t.TempDir()
	var open = func(flag string) *gui {
		t.Helper()
		var g, err = newGUI(Options{DataDir: dir, Network: "regtest", Peer: flag}, test.NewWindow(nil))
		if err != nil { t.Fatalf("newGUI: %v", err) }
		return g
	}
	var g = open("")
	if got := g.pinnedPeer(); got != "" {
		t.Fatalf("pinned peer without flag or setting = %q", got)
	}
	if err := g.store.SetSetting(storage.SettingPinnedPeer, "192.0.2.1:18444"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	g.store.Close()
	g = open("")
	if got := g.pinnedPeer(); got != "192.0.2.1:18444" {
		t.Fatalf("pinned peer from the settings = %q", got)
	}
	g.store.Close()
	g = open("198.51.100.7:18444")
	defer g.store.Close()
	if got := g.pinnedPeer(); got != "198.51.100.7:18444" {
		t.Fatalf("pinned peer with the flag = %q, want the flag", got)
	}
}
