package gui

import "strings"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"

// balanceLayout lays out the balance rows: the balance centred on the first
// row with the pending note right after it, the text of both on one
// baseline, and the USD value on the second row. The note's width is reserved
// on both sides, so the balance stays centred under the address whether the
// note is shown or not. The USD value ends its number where the balance ends
// its number, the units of both hanging past that edge.
type balanceLayout struct{}

// Text styles of the balance, the pending note and the USD value.
var balanceSize, balanceStyle = theme.SizeNameHeadingText, fyne.TextStyle{Bold: true}
var noteSize, noteStyle = theme.SizeNameCaptionText, fyne.TextStyle{}
var usdSize, usdStyle = theme.SizeNameSubHeadingText, fyne.TextStyle{}

// MinSize is wide enough for the balance with the note width on either side
// and for the USD value on both sides of the centre line, and as tall as the
// first row over the USD value.
func (balanceLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var main = objects[0].MinSize()
	var note = visibleMinSize(objects[1])
	var usd = visibleMinSize(objects[2])
	var half = main.Width/2 + note.Width
	if objects[2].Visible() {
		var right = usdRight(objects, 0)
		half = max(half, abs(right), abs(right-usd.Width))
	}
	return fyne.NewSize(2*half, max(main.Height, note.Height)+usd.Height)
}

// Layout centres the balance on the bottom of the first row and puts the note
// at its right, raised or lowered so both texts share the baseline: bottom
// aligned boxes would sit the smaller text visibly lower. Both widgets pad
// their text alike, so only the baselines of the two text sizes differ. The
// USD value goes under them, placed by usdRight.
func (balanceLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var main = objects[0].MinSize()
	var note = objects[1].MinSize()
	var usd = objects[2].MinSize()
	var row = size.Height - visibleMinSize(objects[2]).Height
	var center = size.Width / 2
	var x = center - main.Width/2
	var y = row - main.Height
	objects[0].Resize(main)
	objects[0].Move(fyne.NewPos(x, y))
	objects[1].Resize(note)
	objects[1].Move(fyne.NewPos(x+main.Width, y+textBaseline(balanceSize, balanceStyle)-textBaseline(noteSize, noteStyle)))
	objects[2].Resize(usd)
	objects[2].Move(fyne.NewPos(usdRight(objects, center)-usd.Width, row))
}

// usdRight is the right edge of the USD widget when the balance is centred on
// center: the right edge of the balance, moved left by the balance unit and
// right by the USD unit, so both numbers end at the same x. Both widgets pad
// their text alike, so the paddings cancel out.
func usdRight(objects []fyne.CanvasObject, center float32) float32 {
	return center + objects[0].MinSize().Width/2 -
		unitWidth(objects[0], balanceSize, balanceStyle) +
		unitWidth(objects[2], usdSize, usdStyle)
}

// unitWidth is the width the unit ending the text of a label or rich text
// takes, with the space before it: the rendered width of the text less that
// of its number. It is 0 for other objects, text without a unit, or when no
// app is running to measure it.
func unitWidth(o fyne.CanvasObject, size fyne.ThemeSizeName, style fyne.TextStyle) float32 {
	var text string
	switch w := o.(type) {
	case *widget.Label:
		text = w.Text
	case *widget.RichText:
		text = w.String()
	}
	var i = strings.LastIndexByte(text, ' ')
	if i < 0 {
		return 0
	}
	return textWidth(text, size, style) - textWidth(text[:i], size, style)
}

// textWidth is the rendered width of text at the theme size, or 0 when no
// app is running to measure it.
func textWidth(text string, size fyne.ThemeSizeName, style fyne.TextStyle) float32 {
	var app = fyne.CurrentApp()
	if app == nil {
		return 0
	}
	var measured, _ = app.Driver().RenderedTextSize(text, theme.Size(size), style, nil)
	return measured.Width
}

// textBaseline is the distance from the top of a line of text at the theme
// size to its baseline, or 0 when no app is running to measure it.
func textBaseline(size fyne.ThemeSizeName, style fyne.TextStyle) float32 {
	var app = fyne.CurrentApp()
	if app == nil {
		return 0
	}
	var _, baseline = app.Driver().RenderedTextSize("0", theme.Size(size), style, nil)
	return baseline
}

func visibleMinSize(o fyne.CanvasObject) fyne.Size {
	if !o.Visible() {
		return fyne.Size{}
	}
	return o.MinSize()
}

func abs(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
