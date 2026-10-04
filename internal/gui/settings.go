package gui

import "context"
import "errors"
import "log"
import "net"
import "strconv"
import "strings"
import "syscall"
import "time"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/container"
import "fyne.io/fyne/v2/dialog"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "bitfyn/internal/p2p"
import "bitfyn/internal/storage"
import "bitfyn/internal/sync"
import "bitfyn/internal/tor"

// peerCheckDelay is how long the pinned peer field waits after an edit
// before it connects to the typed peer, so typing does not dial every
// partial address.
const peerCheckDelay = 700 * time.Millisecond

// torClient is the running Tor client: it dials onion peers once ready.
type torClient interface {
	p2p.Dialer
	Close()
}

// settingsView is the Settings tab: the pinned peer, the Tor switch and the
// I2P switch, not available yet. A pinned peer typed in is connected to, and
// once it completed the handshake and serves compact filters it is saved and
// the sync is pinned to it at once. Turning Tor on starts the built-in Tor
// client and has the sync drop its peers, the pinned one included, for onion
// peers; the pinned peer is greyed out until Tor is turned off again.
type settingsView struct {
	content   fyne.CanvasObject
	peerLabel *widget.Label
	peer      *widget.Entry
	status    *widget.RichText
	note      *widget.RichText
	peerNote  []string
	torSwitch *Switch
	torStatus *widget.RichText
	i2p       *Switch
	store     *storage.Store
	window    fyne.Window
	port      string
	inUse     string
	probe     func(ctx context.Context, addr string) error
	apply     func(addr string) error
	delay     time.Duration
	cancel    context.CancelFunc
	edits     int
	checked   func()
	newTor    func(onState func(tor.State, error)) torClient
	setTor    func(on bool, dialer p2p.Dialer)
	torClient torClient
	torStarts int
}

// newSettingsView builds the Settings tab with the pinned peer in use in its
// field, from the -peer flag or the settings. The field is on the line of
// its title, the connection status of the typed peer and a note under it;
// the Tor and I2P switches follow their titles.
func newSettingsView(g *gui) *settingsView {
	var params = g.wallet.Net()
	var v = &settingsView{
		store:  g.store,
		window: g.window,
		port:   params.DefaultPort,
		inUse:  g.pinnedPeer(),
		delay:  peerCheckDelay,
		probe: func(ctx context.Context, addr string) error {
			return p2p.Probe(ctx, params, addr)
		},
		apply: sync.SetPinned,
		newTor: func(onState func(tor.State, error)) torClient {
			return tor.Start(g.dataDir, onState)
		},
		setTor:   sync.SetTor,
		peerNote: []string{"Headers and filters are synced from this peer.", "Leave it empty to choose peers automatically."},
	}
	if g.flagPeer != "" {
		v.peerNote = []string{"Set by the -peer flag for this run.", "A peer confirmed here takes over; the flag wins at the next start."}
	}
	v.peerLabel = widget.NewLabel("Pinned peer")
	v.peer = widget.NewEntry()
	v.peer.SetPlaceHolder("IP address or IP:port")
	v.peer.SetText(v.inUse)
	v.peer.OnChanged = v.edited
	v.status = widget.NewRichText()
	v.note = caption(v.peerNote...)
	v.torSwitch = NewSwitch(nil)
	if on, err := g.store.Setting(storage.SettingTor); err != nil {
		log.Printf("settings: read Tor: %v", err)
	} else {
		v.torSwitch.On = on == "on"
	}
	v.torSwitch.OnChanged = v.torSwitched
	v.torStatus = widget.NewRichText()
	v.i2p = NewSwitch(nil)
	v.i2p.Disable()
	var peerRow = container.NewBorder(nil, nil, v.peerLabel, nil,
		container.NewVBox(v.peer, v.status, v.note))
	v.content = container.NewPadded(container.NewVBox(
		peerRow,
		widget.NewSeparator(),
		switchRow("Tor", v.torSwitch),
		v.torStatus,
		switchRow("I2P", v.i2p),
		caption("I2P is not available yet."),
	))
	v.showTor(tor.StateStarting, nil)
	v.showPinned()
	return v
}

// torOn reports whether the Tor switch is on.
func (v *settingsView) torOn() bool {
	return v.torSwitch.On
}

// startup starts the Tor client when Tor is on, before the sync starts, so
// the sync dials onion peers only from the first.
func (v *settingsView) startup() {
	if v.torOn() { v.startTor() }
}

// torSwitched saves the Tor switch and applies it at once: on, the sync is
// unpinned and the Tor client started; off, the Tor client is closed and the
// sync is pinned to the pinned peer again.
func (v *settingsView) torSwitched(on bool) {
	var value = ""
	if on { value = "on" }
	if err := v.store.SetSetting(storage.SettingTor, value); err != nil {
		dialog.ShowError(err, v.window)
	}
	if on {
		v.stop()
		if err := v.apply(""); err != nil { log.Printf("settings: unpin for Tor: %v", err) }
		v.startTor()
		log.Printf("settings: Tor on")
	} else {
		v.closeTor()
		if err := v.apply(v.inUse); err != nil { dialog.ShowError(err, v.window) }
		log.Printf("settings: Tor off")
	}
	v.showPinned()
	if !on { v.recheck() }
}

// startTor switches the sync to onion peers and starts the Tor client; the
// sync dials through it once it is ready.
func (v *settingsView) startTor() {
	v.torStarts++
	var start = v.torStarts
	v.setTor(true, nil)
	v.torClient = v.newTor(func(state tor.State, err error) {
		fyne.Do(func() { v.torChanged(start, state, err) })
	})
	v.showTor(tor.StateStarting, nil)
}

// torChanged shows the state of the Tor client, unless Tor was switched
// since, and hands the client to the sync once it is ready.
func (v *settingsView) torChanged(start int, state tor.State, err error) {
	if start != v.torStarts || v.torClient == nil { return }
	if state == tor.StateReady { v.setTor(true, v.torClient) }
	v.showTor(state, err)
}

// closeTor switches the sync back to direct peers and closes the Tor client
// in the background.
func (v *settingsView) closeTor() {
	v.torStarts++
	v.setTor(false, nil)
	if v.torClient != nil {
		go v.torClient.Close()
		v.torClient = nil
	}
	v.showTor(tor.StateStarting, nil)
}

// showTor shows the Tor state under the switch: what the switch does while
// it is off, and how far the Tor client got while it is on.
func (v *settingsView) showTor(state tor.State, err error) {
	var text, color = "Only .onion peers, through the built-in Tor client.", theme.ColorNamePlaceHolder
	switch {
	case !v.torOn():
	case state == tor.StateReady:
		text, color = "Tor ready", theme.ColorNameSuccess
	case state == tor.StateFailed:
		text = "Tor failed to start, retrying"
	default:
		text = "Starting Tor..."
	}
	v.torStatus.Segments = []widget.RichTextSegment{&widget.TextSegment{Text: text, Style: widget.RichTextStyle{ColorName: color}}}
	v.torStatus.Refresh()
}

// showPinned greys the pinned peer out while Tor is on, and shows it as
// usual while it is off.
func (v *settingsView) showPinned() {
	if v.torOn() {
		v.peerLabel.Importance = widget.LowImportance
		v.peer.Disable()
		v.setStatus("", theme.ColorNamePlaceHolder)
		v.note.Segments = caption("Not used while Tor is on.").Segments
	} else {
		v.peerLabel.Importance = widget.MediumImportance
		v.peer.Enable()
		v.note.Segments = caption(v.peerNote...).Segments
	}
	v.peerLabel.Refresh()
	v.note.Refresh()
}

// caption is small grey text, one line for each of the lines.
func caption(lines ...string) *widget.RichText {
	var segments = make([]widget.RichTextSegment, len(lines))
	for i, line := range lines {
		segments[i] = &widget.TextSegment{Text: line, Style: captionStyle}
	}
	return widget.NewRichText(segments...)
}

var captionStyle = widget.RichTextStyle{
	ColorName: theme.ColorNamePlaceHolder,
	SizeName:  theme.SizeNameCaptionText,
}

// switchRow puts the switch right after its name.
func switchRow(name string, s *Switch) fyne.CanvasObject {
	return container.NewHBox(widget.NewLabel(name), s)
}

// recheck connects to the peer in the field again, refreshing its status,
// unless Tor is on.
func (v *settingsView) recheck() {
	if v.torOn() { return }
	v.edited(v.peer.Text)
}

// edited checks the typed peer: an empty field unpins the sync, an address
// that is not an IP, with a port or without, is only reported, and an IP
// address is connected to after peerCheckDelay. A newer edit abandons the
// check under way.
func (v *settingsView) edited(text string) {
	v.stop()
	text = strings.TrimSpace(text)
	if text == "" {
		v.setStatus("", theme.ColorNamePlaceHolder)
		v.use("")
		return
	}
	var addr, ok = parsePeer(text, v.port)
	if !ok {
		v.setStatus("Not connected: not an IP address or IP:port", theme.ColorNamePlaceHolder)
		return
	}
	var ctx, cancel = context.WithCancel(context.Background())
	var edit = v.edits
	v.cancel = cancel
	v.setStatus("Connecting to "+addr+"…", theme.ColorNamePlaceHolder)
	go func() {
		defer cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(v.delay):
		}
		var err = v.probe(ctx, addr)
		fyne.Do(func() { v.probed(edit, addr, err) })
	}()
}

// probed shows the outcome of the check of the peer, unless the field was
// edited since, and pins the sync to the peer once it connected.
func (v *settingsView) probed(edit int, addr string, err error) {
	if edit != v.edits { return }
	v.cancel = nil
	if err != nil {
		log.Printf("settings: pinned peer %s: %v", addr, err)
		v.setStatus("Not connected: "+probeReason(err), theme.ColorNamePlaceHolder)
	} else {
		v.setStatus("Connected", theme.ColorNameSuccess)
		v.use(addr)
	}
	if v.checked != nil { v.checked() }
}

// stop abandons the check under way, if any.
func (v *settingsView) stop() {
	v.edits++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
}

// use saves the pinned peer, an empty one removing it, and pins the sync to
// it right away, unless it is the peer already in use.
func (v *settingsView) use(addr string) {
	if addr == v.inUse { return }
	if err := v.store.SetSetting(storage.SettingPinnedPeer, addr); err != nil {
		dialog.ShowError(err, v.window)
		return
	}
	if err := v.apply(addr); err != nil {
		dialog.ShowError(err, v.window)
		return
	}
	v.inUse = addr
	if addr == "" {
		log.Printf("settings: pinned peer removed")
	} else {
		log.Printf("settings: pinned peer %s saved and in use", addr)
	}
}

// setStatus shows the status line under the field in the colour. The line
// keeps its height while empty, so the rows under it do not move.
func (v *settingsView) setStatus(text string, color fyne.ThemeColorName) {
	v.status.Segments = []widget.RichTextSegment{&widget.TextSegment{Text: text, Style: widget.RichTextStyle{ColorName: color}}}
	v.status.Refresh()
}

// statusText is the status line shown under the field.
func (v *settingsView) statusText() string {
	return v.status.String()
}

// parsePeer reads a peer typed as an IP address with a port, or without one,
// then taking the network's default port. It returns the address in the
// host:port form.
func parsePeer(text, defaultPort string) (string, bool) {
	var bare = text
	if strings.HasPrefix(bare, "[") && strings.HasSuffix(bare, "]") {
		bare = bare[1 : len(bare)-1]
	}
	if ip := net.ParseIP(bare); ip != nil {
		return net.JoinHostPort(ip.String(), defaultPort), true
	}
	var host, port, err = net.SplitHostPort(text)
	if err != nil { return "", false }
	var ip = net.ParseIP(host)
	var num, perr = strconv.ParseUint(port, 10, 16)
	if ip == nil || perr != nil || num == 0 { return "", false }
	return net.JoinHostPort(ip.String(), strconv.FormatUint(num, 10)), true
}

// probeReason says briefly why a peer could not be connected to.
func probeReason(err error) string {
	var netErr net.Error
	var errno syscall.Errno
	switch {
	case errors.Is(err, p2p.ErrNoCompactFilters):
		return "no compact filters (needs blockfilterindex=1 and peerblockfilters=1)"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "no answer"
	case errors.As(err, &errno):
		return errno.Error()
	default:
		return "no handshake"
	}
}
