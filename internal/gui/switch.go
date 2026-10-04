package gui

import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/canvas"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"

const switchWidth = 36
const switchHeight = 20
const knobInset = 2

// Switch is an on/off toggle, Fyne having none of its own: a rounded track
// with a round knob, at the right and the track in the primary colour while
// on. Tapping it flips it unless it is disabled.
type Switch struct {
	widget.DisableableWidget
	On        bool
	OnChanged func(on bool)
}

// NewSwitch creates a switch, off, calling changed whenever it flips.
func NewSwitch(changed func(on bool)) *Switch {
	var s = &Switch{OnChanged: changed}
	s.ExtendBaseWidget(s)
	return s
}

// SetOn sets the switch and reports a change to OnChanged.
func (s *Switch) SetOn(on bool) {
	if s.On == on { return }
	s.On = on
	s.Refresh()
	if s.OnChanged != nil { s.OnChanged(on) }
}

// Tapped flips the switch unless it is disabled.
func (s *Switch) Tapped(*fyne.PointEvent) {
	if s.Disabled() { return }
	s.SetOn(!s.On)
}

func (s *Switch) CreateRenderer() fyne.WidgetRenderer {
	var r = &switchRenderer{sw: s, track: canvas.NewRectangle(nil), knob: canvas.NewCircle(nil)}
	r.track.CornerRadius = switchHeight / 2
	r.Refresh()
	return r
}

type switchRenderer struct {
	sw    *Switch
	track *canvas.Rectangle
	knob  *canvas.Circle
}

func (r *switchRenderer) MinSize() fyne.Size {
	return fyne.NewSize(switchWidth, switchHeight)
}

// Layout centres the track vertically, the knob inside it at the left while
// off and at the right while on.
func (r *switchRenderer) Layout(size fyne.Size) {
	var top = (size.Height - switchHeight) / 2
	r.track.Resize(fyne.NewSize(switchWidth, switchHeight))
	r.track.Move(fyne.NewPos(0, top))
	var knob = float32(switchHeight - 2*knobInset)
	var x = float32(knobInset)
	if r.sw.On { x = switchWidth - knobInset - knob }
	r.knob.Resize(fyne.NewSize(knob, knob))
	r.knob.Move(fyne.NewPos(x, top+knobInset))
}

func (r *switchRenderer) Refresh() {
	var th = r.sw.Theme()
	var v = fyne.CurrentApp().Settings().ThemeVariant()
	var track, knob = theme.ColorNameInputBorder, theme.ColorNameForegroundOnPrimary
	switch {
	case r.sw.Disabled():
		track, knob = theme.ColorNameDisabledButton, theme.ColorNameDisabled
	case r.sw.On:
		track = theme.ColorNamePrimary
	}
	r.track.FillColor = th.Color(track, v)
	r.knob.FillColor = th.Color(knob, v)
	r.Layout(r.sw.Size())
	r.track.Refresh()
	r.knob.Refresh()
}

func (r *switchRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.track, r.knob}
}

func (r *switchRenderer) Destroy() {}
