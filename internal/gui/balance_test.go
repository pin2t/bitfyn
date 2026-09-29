package gui

import "testing"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/canvas"
import "fyne.io/fyne/v2/theme"

// TestBalanceLayout checks that the balance is centred on the bottom of the
// row, the note sits right after it on the balance's text baseline, and a
// hidden note reserves no width.
func TestBalanceLayout(t *testing.T) {
	var main = canvas.NewRectangle(nil)
	main.SetMinSize(fyne.NewSize(100, 40))
	var note = canvas.NewRectangle(nil)
	note.SetMinSize(fyne.NewSize(30, 16))
	var objects = []fyne.CanvasObject{main, note}
	if got := (balanceLayout{}).MinSize(objects); got != fyne.NewSize(160, 40) {
		t.Fatalf("MinSize = %v, want 160x40", got)
	}
	balanceLayout{}.Layout(objects, fyne.NewSize(300, 50))
	if main.Position() != fyne.NewPos(100, 10) || main.Size() != fyne.NewSize(100, 40) {
		t.Fatalf("balance at %v size %v, want (100,10) 100x40", main.Position(), main.Size())
	}
	var mainBase = textBaseline(theme.SizeNameHeadingText, fyne.TextStyle{Bold: true})
	var noteBase = textBaseline(theme.SizeNameCaptionText, fyne.TextStyle{})
	if mainBase <= noteBase {
		t.Fatalf("heading baseline %v not below caption baseline %v", mainBase, noteBase)
	}
	if note.Position().X != 200 || note.Position().Y+noteBase != main.Position().Y+mainBase {
		t.Fatalf("note at %v, want x 200 on the balance baseline", note.Position())
	}
	note.Hide()
	if got := (balanceLayout{}).MinSize(objects); got != fyne.NewSize(100, 40) {
		t.Fatalf("MinSize with hidden note = %v, want 100x40", got)
	}
}
