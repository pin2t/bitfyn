package gui

import "strings"
import "testing"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/canvas"
import "fyne.io/fyne/v2/test"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"

// TestBalanceLayout checks that the balance is centred on the bottom of the
// row, the note sits right after it on the balance's text baseline, a
// hidden note reserves no width, and a hidden USD value takes no room.
func TestBalanceLayout(t *testing.T) {
	var main = canvas.NewRectangle(nil)
	main.SetMinSize(fyne.NewSize(100, 40))
	var note = canvas.NewRectangle(nil)
	note.SetMinSize(fyne.NewSize(30, 16))
	var usd = canvas.NewRectangle(nil)
	usd.SetMinSize(fyne.NewSize(80, 20))
	usd.Hide()
	var objects = []fyne.CanvasObject{main, note, usd}
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

// TestBalanceLayoutUSD checks that the USD value sits under the balance,
// raised into its padding with the space left under it, with the right ends
// of both numbers at the same x, the units hanging past it.
func TestBalanceLayoutUSD(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	for _, text := range []string{"1 234 567 sats", "0.12345678 BTC", "21 BTC"} {
		var main = widget.NewLabelWithStyle(text, fyne.TextAlignCenter, balanceStyle)
		main.SizeName = balanceSize
		var note = widget.NewRichText(&widget.TextSegment{Text: "(1 sats pending)", Style: pendingStyle})
		var usd = widget.NewRichText(&widget.TextSegment{Text: "12 345.67 USD", Style: usdTextStyle})
		var objects = []fyne.CanvasObject{main, note, usd}
		if unitWidth(main, balanceSize, balanceStyle) <= 0 || unitWidth(usd, usdSize, usdStyle) <= 0 {
			t.Fatalf("%s: units not measured", text)
		}
		var min = (balanceLayout{}).MinSize(objects)
		var first = max(main.MinSize().Height, note.MinSize().Height)
		if min.Height != first+usd.MinSize().Height {
			t.Fatalf("%s: MinSize height %v, want %v", text, min.Height, first+usd.MinSize().Height)
		}
		var size = fyne.NewSize(600, min.Height)
		balanceLayout{}.Layout(objects, size)
		if main.Position().Y+main.Size().Height != first || usd.Position().Y != first-usdLift() {
			t.Fatalf("%s: balance ends at %v, USD starts at %v, want %v and %v", text, main.Position().Y+main.Size().Height, usd.Position().Y, first, first-usdLift())
		}
		if gap := size.Height - (usd.Position().Y + usd.Size().Height); gap != usdLift() {
			t.Fatalf("%s: %v left under the USD value, want %v", text, gap, usdLift())
		}
		var number = strings.LastIndexByte(text, ' ')
		var mainEnd = main.Position().X + theme.InnerPadding() + textWidth(text[:number], balanceSize, balanceStyle)
		var usdEnd = usd.Position().X + theme.InnerPadding() + textWidth("12 345.67", usdSize, usdStyle)
		if d := mainEnd - usdEnd; d < -0.5 || d > 0.5 {
			t.Errorf("%s: balance number ends at %v, USD number at %v", text, mainEnd, usdEnd)
		}
		if usd.Position().X < 0 || usd.Position().X+usd.Size().Width > size.Width {
			t.Errorf("%s: USD at %v size %v outside the row", text, usd.Position(), usd.Size())
		}
	}
}
