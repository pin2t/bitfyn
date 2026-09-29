package gui

import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/theme"

// balanceLayout lays out the balance row: the balance centred in the row and
// the pending note right after it, the text of both on one baseline. The
// note's width is reserved on both sides, so the balance stays centred under
// the address whether the note is shown or not.
type balanceLayout struct{}

// MinSize is the balance width plus the note width on either side, and the
// taller of the two.
func (balanceLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var main = objects[0].MinSize()
	var note = visibleMinSize(objects[1])
	return fyne.NewSize(main.Width+2*note.Width, max(main.Height, note.Height))
}

// Layout centres the balance on the bottom of the row and puts the note at
// its right, raised or lowered so both texts share the baseline: bottom
// aligned boxes would sit the smaller text visibly lower. Both widgets pad
// their text alike, so only the baselines of the two text sizes differ.
func (balanceLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var main = objects[0].MinSize()
	var note = objects[1].MinSize()
	var x = (size.Width - main.Width) / 2
	var y = size.Height - main.Height
	var mainBase = textBaseline(theme.SizeNameHeadingText, fyne.TextStyle{Bold: true})
	var noteBase = textBaseline(theme.SizeNameCaptionText, fyne.TextStyle{})
	objects[0].Resize(main)
	objects[0].Move(fyne.NewPos(x, y))
	objects[1].Resize(note)
	objects[1].Move(fyne.NewPos(x+main.Width, y+mainBase-noteBase))
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
