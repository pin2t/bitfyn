package gui

import "context"
import "fmt"
import "net"
import "syscall"
import "testing"
import "time"
import "fyne.io/fyne/v2/test"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "bitfyn/internal/p2p"
import "bitfyn/internal/storage"
import "bitfyn/internal/tor"

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

// fakeTor stands in for the Tor client and tells when it is closed.
type fakeTor struct {
	closed chan struct{}
}

func (f *fakeTor) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, fmt.Errorf("fake Tor dials nothing")
}

func (f *fakeTor) Close() { close(f.closed) }

// TestSettingsTor turns Tor on and off. On, it is saved, the sync is
// unpinned and switched to Tor, the pinned peer is greyed out and not
// checked, and the Tor client goes to the sync once ready. Off, the client
// is closed, the sync is switched back and pinned again, and the pinned peer
// is checked again. A late state of the closed client changes nothing.
func TestSettingsTor(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var w = test.NewWindow(nil)
	defer w.Close()
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil { t.Fatalf("newGUI: %v", err) }
	defer g.store.Close()
	if err := g.store.SetSetting(storage.SettingPinnedPeer, "192.0.2.1:18444"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	var v, checked, applied = testSettings(g)
	var client = &fakeTor{closed: make(chan struct{})}
	var onState func(tor.State, error)
	v.newTor = func(on func(tor.State, error)) torClient {
		onState = on
		return client
	}
	var modes []string
	v.setTor = func(on bool, dialer p2p.Dialer) {
		modes = append(modes, fmt.Sprintf("on=%v dialer=%v", on, dialer != nil))
	}
	var expect = func(setting, pinned, mode, torStatus string, grey bool) {
		t.Helper()
		if got, _ := g.store.Setting(storage.SettingTor); got != setting {
			t.Fatalf("Tor setting %q, want %q", got, setting)
		}
		if got := (*applied)[len(*applied)-1]; got != pinned {
			t.Fatalf("sync pinned to %q, want %q", got, pinned)
		}
		if got := modes[len(modes)-1]; got != mode {
			t.Fatalf("sync Tor mode %q, want %q", got, mode)
		}
		if got := v.torStatus.String(); got != torStatus {
			t.Fatalf("Tor status %q, want %q", got, torStatus)
		}
		if v.peer.Disabled() != grey || (v.peerLabel.Importance == widget.LowImportance) != grey {
			t.Fatalf("pinned peer greyed out = %v, want %v", v.peer.Disabled(), grey)
		}
	}
	test.Tap(v.torSwitch)
	expect("on", "", "on=true dialer=false", "Starting Tor...", true)
	onState(tor.StateReady, nil)
	expect("on", "", "on=true dialer=true", "Tor ready", true)
	v.recheck()
	select {
	case <-checked:
		t.Fatalf("pinned peer checked while Tor is on")
	case <-time.After(50 * time.Millisecond):
	}
	test.Tap(v.torSwitch)
	expect("", "192.0.2.1:18444", "on=false dialer=false", "Only .onion peers, through the built-in Tor client.", false)
	select {
	case <-client.closed:
	case <-time.After(5 * time.Second):
		t.Fatalf("Tor client not closed")
	}
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Fatalf("pinned peer not checked again after Tor was turned off")
	}
	var count = len(modes)
	onState(tor.StateReady, nil)
	if len(modes) != count {
		t.Fatalf("a late state of the closed Tor client switched the sync: %v", modes[count:])
	}
}

// TestSettingsTorStartup checks that Tor saved on is started at startup.
func TestSettingsTorStartup(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var w = test.NewWindow(nil)
	defer w.Close()
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil { t.Fatalf("newGUI: %v", err) }
	defer g.store.Close()
	if err := g.store.SetSetting(storage.SettingTor, "on"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	var v, _, _ = testSettings(g)
	var started = false
	v.newTor = func(func(tor.State, error)) torClient {
		started = true
		return &fakeTor{closed: make(chan struct{})}
	}
	v.setTor = func(bool, p2p.Dialer) {}
	if !v.torOn() || !v.peer.Disabled() {
		t.Fatalf("Tor on = %v, pinned peer disabled = %v; want both", v.torOn(), v.peer.Disabled())
	}
	v.startup()
	if !started {
		t.Fatalf("Tor not started at startup")
	}
}
