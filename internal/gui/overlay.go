package gui

import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "bitfyn/internal/p2p"

// overlayClient is the running client of an anonymity network, Tor or I2P:
// it dials the network's peers once ready.
type overlayClient interface {
	p2p.Dialer
	Close()
}

// overlay is the switch of one anonymity network, Tor or I2P, in the
// Settings tab, with its status right after it, and the network's client
// while it is on. The texts say what the switch does while it is off, and
// that the client starts, is ready or failed while it is on.
type overlay struct {
	view     *settingsView
	network  p2p.Network
	setting  string
	idle     string
	starting string
	failed   func(err error) string
	start    func(onState func(p2p.State, error)) overlayClient
	sw       *Switch
	status   *widget.RichText
	client   overlayClient
	starts   int
}

// newOverlay builds the switch of the network, on as saved in the setting,
// calling switched when it is flipped.
func newOverlay(v *settingsView, network p2p.Network, setting string, switched func(o *overlay, on bool)) *overlay {
	var o = &overlay{view: v, network: network, setting: setting, status: widget.NewRichText()}
	o.sw = NewSwitch(func(on bool) { switched(o, on) })
	return o
}

// on reports whether the switch is on.
func (o *overlay) on() bool {
	return o.sw.On
}

// begin turns the network on for the sync and starts its client; the sync
// dials through the client once it is ready.
func (o *overlay) begin() {
	o.starts++
	var start = o.starts
	o.view.setOverlay(o.network, true, nil)
	o.client = o.start(func(state p2p.State, err error) {
		fyne.Do(func() { o.changed(start, state, err) })
	})
	o.show(p2p.StateStarting, nil)
}

// changed shows the state of the client, unless the switch was flipped
// since, and hands the client to the sync once it is ready.
func (o *overlay) changed(start int, state p2p.State, err error) {
	if start != o.starts || o.client == nil { return }
	if state == p2p.StateReady { o.view.setOverlay(o.network, true, o.client) }
	o.show(state, err)
}

// end turns the network off for the sync and closes its client in the
// background.
func (o *overlay) end() {
	o.starts++
	o.view.setOverlay(o.network, false, nil)
	if o.client != nil {
		go o.client.Close()
		o.client = nil
	}
	o.show(p2p.StateStarting, nil)
}

// show puts the status after the switch: what the switch does while it is
// off, how far the client got while it is on, in green once it is ready.
func (o *overlay) show(state p2p.State, err error) {
	var text, color = o.idle, theme.ColorNamePlaceHolder
	switch {
	case !o.on():
	case state == p2p.StateReady:
		text, color = o.network.String()+" ready", theme.ColorNameSuccess
	case state == p2p.StateFailed:
		text = o.failed(err)
	default:
		text = o.starting
	}
	o.status.Segments = []widget.RichTextSegment{&widget.TextSegment{Text: text, Style: widget.RichTextStyle{ColorName: color}}}
	o.status.Refresh()
}
