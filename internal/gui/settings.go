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

// peerCheckDelay is how long the pinned peer field waits after an edit
// before it connects to the typed peer, so typing does not dial every
// partial address.
const peerCheckDelay = 700 * time.Millisecond

// settingsView is the Settings tab: the pinned peer, and the Tor and I2P
// switches, not available yet. A pinned peer typed in is connected to, and
// once it completed the handshake and serves compact filters it is saved and
// the sync is pinned to it at once.
type settingsView struct {
	content fyne.CanvasObject
	peer    *widget.Entry
	status  *widget.RichText
	tor     *Switch
	i2p     *Switch
	store   *storage.Store
	window  fyne.Window
	port    string
	inUse   string
	probe   func(ctx context.Context, addr string) error
	apply   func(addr string) error
	delay   time.Duration
	cancel  context.CancelFunc
	edits   int
	checked func()
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
	}
	v.peer = widget.NewEntry()
	v.peer.SetPlaceHolder("IP address or IP:port")
	v.peer.SetText(v.inUse)
	v.peer.OnChanged = v.edited
	v.status = widget.NewRichText()
	var note = caption("Headers and filters are synced from this peer.", "Leave it empty to choose peers automatically.")
	if g.flagPeer != "" {
		note = caption("Set by the -peer flag for this run.", "A peer confirmed here takes over; the flag wins at the next start.")
	}
	v.tor = NewSwitch(nil)
	v.tor.Disable()
	v.i2p = NewSwitch(nil)
	v.i2p.Disable()
	var peerRow = container.NewBorder(nil, nil, widget.NewLabel("Pinned peer"), nil,
		container.NewVBox(v.peer, v.status, note))
	v.content = container.NewPadded(container.NewVBox(
		peerRow,
		widget.NewSeparator(),
		switchRow("Tor", v.tor),
		switchRow("I2P", v.i2p),
		caption("Tor and I2P are not available yet."),
	))
	return v
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

// recheck connects to the peer in the field again, refreshing its status.
func (v *settingsView) recheck() {
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
