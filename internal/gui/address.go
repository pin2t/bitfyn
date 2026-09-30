package gui

import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/theme"

// addressLayout lays out the address row: the address centred in the row,
// under the QR code, and the copy button after it with its icon one padding
// after the end of the address text. The button's width is reserved on both
// sides so the address stays centred. The objects are the address label and
// the copy button.
type addressLayout struct{}

// MinSize is the address width with the room the button takes past it on
// either side, and the taller of the two.
func (addressLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var addr = objects[0].MinSize()
	var button = objects[1].MinSize()
	var past = copyOffset(objects[1]) + button.Width - theme.InnerPadding()
	return fyne.NewSize(addr.Width+2*max(past, 0), max(addr.Height, button.Height))
}

// Layout centres the address and places the button after its text, both
// centred vertically.
func (addressLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var addr = objects[0].MinSize()
	var button = objects[1].MinSize()
	var x = (size.Width - addr.Width) / 2
	objects[0].Resize(addr)
	objects[0].Move(fyne.NewPos(x, (size.Height-addr.Height)/2))
	objects[1].Resize(button)
	objects[1].Move(fyne.NewPos(x+addr.Width-theme.InnerPadding()+copyOffset(objects[1]), (size.Height-button.Height)/2))
}

// copyOffset is where the button goes relative to the end of the address
// text: one padding after it, less the padding the button keeps around its
// icon, so the gap is to the icon itself.
func copyOffset(button fyne.CanvasObject) float32 {
	var inset = (button.MinSize().Width - theme.IconInlineSize()) / 2
	return theme.Padding() - inset
}
