package gui

import "image/color"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/canvas"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"

// signalBars is the number of bars of the connectivity indicator.
const signalBars = 3

const barWidth = 6
const barGap = 3
const barStep = 6
const barBase = 8

// SignalWidget draws the network connectivity as three ascending bars: one
// solid bar per connected peer, the remaining bars are outlined only.
type SignalWidget struct {
	widget.BaseWidget
	level int
}

// NewSignalWidget creates the indicator with no connected peers.
func NewSignalWidget() *SignalWidget {
	var s = &SignalWidget{}
	s.ExtendBaseWidget(s)
	return s
}

// SetLevel sets how many bars are solid, clamped to the bar count.
func (s *SignalWidget) SetLevel(level int) {
	s.level = max(0, min(level, signalBars))
	s.Refresh()
}

// barSolid reports whether the bar at the index is drawn solid at the level.
func barSolid(level, index int) bool {
	return index < level
}

// barHeight is the height of the bar at the index: each bar is taller than
// the one before.
func barHeight(index int) float32 {
	return float32(barBase + barStep*index)
}

func (s *SignalWidget) CreateRenderer() fyne.WidgetRenderer {
	var r = &signalRenderer{signal: s}
	for i := range r.bars {
		r.bars[i] = canvas.NewRectangle(color.Transparent)
	}
	r.Refresh()
	return r
}

type signalRenderer struct {
	signal *SignalWidget
	bars   [signalBars]*canvas.Rectangle
}

func (r *signalRenderer) MinSize() fyne.Size {
	return fyne.NewSize(signalBars*barWidth+(signalBars-1)*barGap, barHeight(signalBars-1))
}

// Layout aligns the bars to the bottom edge, left to right.
func (r *signalRenderer) Layout(size fyne.Size) {
	for i, bar := range r.bars {
		var h = barHeight(i)
		bar.Resize(fyne.NewSize(barWidth, h))
		bar.Move(fyne.NewPos(float32(i*(barWidth+barGap)), size.Height-h))
	}
}

func (r *signalRenderer) Refresh() {
	var fg = theme.ForegroundColor()
	for i, bar := range r.bars {
		bar.StrokeColor = fg
		bar.StrokeWidth = 1
		if barSolid(r.signal.level, i) {
			bar.FillColor = fg
		} else {
			bar.FillColor = color.Transparent
		}
		bar.Refresh()
	}
}

func (r *signalRenderer) Objects() []fyne.CanvasObject {
	var objects = make([]fyne.CanvasObject, len(r.bars))
	for i, bar := range r.bars {
		objects[i] = bar
	}
	return objects
}

func (r *signalRenderer) Destroy() {}
