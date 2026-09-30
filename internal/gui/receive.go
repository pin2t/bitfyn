package gui

import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/container"
import "fyne.io/fyne/v2/dialog"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"

// receiveForm is the Receive dialog content: an optional amount and the
// BIP21 invoice for the current address as a QR code and as text.
type receiveForm struct {
	address string
	amount  *widget.Entry
	unit    *widget.Select
	qr      *QRWidget
	uri     *widget.Label
	problem *widget.Label
}

// newReceiveForm builds the invoice form for the address.
func newReceiveForm(address string) *receiveForm {
	var f = &receiveForm{address: address}
	f.amount = widget.NewEntry()
	f.amount.SetPlaceHolder("Amount (optional)")
	f.unit = widget.NewSelect([]string{unitSats, unitBTC}, nil)
	f.unit.SetSelected(unitSats)
	f.qr = NewQRWidget(invoiceURI(address, 0))
	f.uri = widget.NewLabelWithStyle(invoiceURI(address, 0), fyne.TextAlignCenter, fyne.TextStyle{Monospace: true})
	f.uri.Wrapping = fyne.TextWrapBreak
	f.problem = widget.NewLabel("")
	f.problem.Importance = widget.DangerImportance
	f.amount.OnChanged = func(string) { f.update() }
	f.unit.OnChanged = func(string) { f.update() }
	return f
}

// update re-renders the invoice for the entered amount, or names the
// problem with it on the line kept free for that under the amount.
func (f *receiveForm) update() {
	var sats, err = parseAmount(f.amount.Text, f.unit.Selected)
	if err != nil {
		f.problem.SetText(err.Error())
		return
	}
	f.problem.SetText("")
	var uri = invoiceURI(f.address, sats)
	_ = f.qr.SetContent(uri)
	f.uri.SetText(uri)
}

// showReceive opens the Receive dialog for the current address.
func (g *gui) showReceive() { g.openReceive() }

// openReceive opens the Receive dialog and returns its form.
func (g *gui) openReceive() *receiveForm {
	var f = newReceiveForm(g.addr.Text)
	var copyBtn = widget.NewButtonWithIcon("Copy invoice", theme.ContentCopyIcon(), func() {
		fyne.CurrentApp().Clipboard().SetContent(f.uri.Text)
	})
	var amountRow = container.NewBorder(nil, nil, nil, f.unit, f.amount)
	var content = container.NewVBox(
		amountRow,
		f.problem,
		container.NewCenter(f.qr),
		f.uri,
		container.NewCenter(copyBtn),
	)
	var d = dialog.NewCustom("Receive", "Close", content, g.window)
	d.Resize(fyne.NewSize(560, 620))
	d.Show()
	return f
}
