// Package gui implements the Fyne user interface of the wallet.
package gui

import "errors"
import "fmt"
import "image/color"
import "log"
import "path/filepath"
import "time"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/app"
import "fyne.io/fyne/v2/canvas"
import "fyne.io/fyne/v2/container"
import "fyne.io/fyne/v2/dialog"
import "fyne.io/fyne/v2/layout"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "github.com/btcsuite/btcd/chaincfg"
import "bitfyn/internal/rates"
import "bitfyn/internal/storage"
import "bitfyn/internal/sync"
import "bitfyn/internal/wallet"

// Options configures the GUI.
type Options struct {
	DataDir string
	Network string
	DBPass  string
	Peer    string
}

// Run starts the Fyne application and blocks until the window is closed.
func Run(opts Options) {
	var a = app.NewWithID("bitfyn.wallet")
	var w = a.NewWindow("BitFyn")
	w.Resize(fyne.NewSize(600, 800))
	w.SetFixedSize(true)
	w.CenterOnScreen()
	var gui, err = newGUI(opts, w)
	if err != nil {
		log.Printf("startup failed: %v", err)
		w.SetContent(container.NewVBox(
			widget.NewLabelWithStyle("BitFyn failed to start", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle(err.Error(), fyne.TextAlignCenter, fyne.TextStyle{Monospace: true}),
		))
		w.ShowAndRun()
		return
	}
	w.SetOnClosed(func() {
		sync.Stop()
		rates.Stop()
		_ = gui.store.Close()
	})
	w.SetContent(gui.tabs(gui.content()))
	a.Lifecycle().SetOnStarted(func() {
		gui.startSync(opts.Peer)
		rates.Start(gui.store, func(r storage.Rate) {
			fyne.Do(func() { gui.setRate(r.Cents) })
		})
	})
	w.ShowAndRun()
}

// gui holds the UI state and the wallet/store backend.
type gui struct {
	window fyne.Window
	store  *storage.Store
	wallet *wallet.Wallet
	net    string
	index uint32
	qr   *QRWidget
	addr *widget.Label
	copyAddr *widget.Button
	balance *widget.Label
	pending *widget.RichText
	usd     *widget.RichText
	balanceRow *fyne.Container
	total int64
	rate  int64
	signal *SignalWidget
	status *widget.Label
	receive *widget.Button
	send    *widget.Button
}

// newGUI opens the database, creating the wallet on first run, and
// prepares the UI state.
func newGUI(opts Options, w fyne.Window) (*gui, error) {
	var net, err = wallet.ParamsForNetwork(opts.Network)
	if err != nil { return nil, err }
	store, err := storage.Open(filepath.Join(opts.DataDir, "bitfyn.db"), opts.DBPass)
	if err != nil { return nil, err }
	var fail = func(err error) (*gui, error) {
		_ = store.Close()
		return nil, err
	}
	if err := rates.Seed(store); err != nil {
		log.Printf("rates: %v", err)
	}
	var rate, rerr = store.LatestRate()
	if rerr != nil && !errors.Is(rerr, storage.ErrNoRate) {
		log.Printf("rates: read latest rate: %v", rerr)
	}
	meta, err := store.Meta()
	var wl *wallet.Wallet
	switch {
	case errors.Is(err, storage.ErrNoWallet):
		var mnemonic, err = wallet.NewMnemonic(128)
		if err != nil {
			return fail(fmt.Errorf("generate mnemonic: %w", err))
		}
		wl, err = wallet.New(mnemonic, "", net)
		if err != nil { return fail(err) }
		xpub, err := wl.AccountXPub()
		if err != nil { return fail(err) }
		if err := store.SaveMeta(mnemonic, xpub, opts.Network, time.Now().Unix()); err != nil {
			return fail(fmt.Errorf("save wallet: %w", err))
		}
		meta, err = store.Meta()
		if err != nil { return fail(err) }
	case err != nil:
		return fail(err)
	default:
		if meta.Network != opts.Network {
			return fail(fmt.Errorf("wallet database is for network %q, not %q", meta.Network, opts.Network))
		}
		wl, err = wallet.New(meta.Mnemonic, "", net)
		if err != nil { return fail(err) }
	}
	return &gui{
		window: w,
		store:  store,
		wallet: wl,
		net:    meta.Network,
		index:  meta.NextIndex,
		rate:   rate.Cents,
	}, nil
}

// content builds the window layout: the title on the top row with the
// connectivity bars at its right, the status line right under the bars, its
// text ending where the highest bar ends, sharing its line with the network
// name off mainnet, then the address QR code and the address text right
// below it, centred under the code, with a clipboard copy icon close after
// the text, then the wallet balance in large type with a small grey note on
// the incoming pending part at its right and the balance in US dollars close
// under it in smaller grey type, all filled in by the sync status, and the
// Receive and Send buttons.
func (g *gui) content() fyne.CanvasObject {
	g.qr = NewQRWidget("")
	g.addr = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Monospace: true})
	g.copyAddr = widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() {
		if g.addr.Text == "" { return }
		fyne.CurrentApp().Clipboard().SetContent(g.addr.Text)
		dialog.ShowInformation("Copied", "Address copied to clipboard", g.window)
	})
	g.copyAddr.Importance = widget.LowImportance
	var title = widget.NewLabelWithStyle("BitFyn", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	var network = fyne.CanvasObject(layout.NewSpacer())
	if g.wallet.Net().Net != chaincfg.MainNetParams.Net {
		network = widget.NewLabelWithStyle("net: "+g.net, fyne.TextAlignCenter, fyne.TextStyle{})
	}
	if err := g.refreshAddress(); err != nil {
		dialog.ShowError(err, g.window)
	}
	g.balance = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	g.balance.SizeName = theme.SizeNameHeadingText
	g.pending = widget.NewRichText(&widget.TextSegment{Style: pendingStyle})
	g.pending.Hide()
	g.usd = widget.NewRichText(&widget.TextSegment{Style: usdTextStyle})
	g.usd.Hide()
	g.balanceRow = container.New(balanceLayout{}, g.balance, g.pending, g.usd)
	g.signal = NewSignalWidget()
	g.status = widget.NewLabel(sync.Status{}.String())
	return container.NewVBox(
		container.New(headerLayout{}, title, signalInset(g.signal), network, g.status),
		container.NewCenter(g.qr),
		container.New(addressLayout{}, g.addr, g.copyAddr),
		g.balanceRow,
		g.actions(),
	)
}

// signalInset pads the indicator on the right by the text padding of the
// status label under it, so its highest bar ends where the status text ends.
func signalInset(signal *SignalWidget) fyne.CanvasObject {
	return container.New(layout.NewCustomPaddedLayout(0, 0, 0, theme.InnerPadding()), signal)
}

// actionWidth is the width of each of the Receive and Send buttons.
const actionWidth = 160

// actions builds the Receive and Send buttons under the balance, Receive at
// the left with an arrow down onto a line, Send at the right with an arrow
// up from a line, both the same width. Receive opens the invoice dialog,
// Send the payment dialog.
func (g *gui) actions() fyne.CanvasObject {
	g.receive = widget.NewButtonWithIcon("Receive", theme.DownloadIcon(), g.showReceive)
	g.send = widget.NewButtonWithIcon("Send", theme.UploadIcon(), g.showSend)
	return container.NewCenter(container.NewGridWithColumns(2, atLeastWide(g.receive, actionWidth), atLeastWide(g.send, actionWidth)))
}

// atLeastWide gives the object a minimum width, keeping its own height.
func atLeastWide(o fyne.CanvasObject, width float32) fyne.CanvasObject {
	var strut = canvas.NewRectangle(color.Transparent)
	strut.SetMinSize(fyne.NewSize(width, 0))
	return container.NewStack(strut, o)
}

// refreshAddress derives the current address, updates QR, labels and the
// address table, and has the sync watch the address.
func (g *gui) refreshAddress() error {
	var address, path, pubkey, err = g.wallet.DeriveAddress(g.index)
	if err != nil {
		return fmt.Errorf("derive address %d: %w", g.index, err)
	}
	if err := g.qr.SetContent(address); err != nil {
		return fmt.Errorf("encode QR: %w", err)
	}
	g.addr.SetText(address)
	if err := g.store.AddAddress(g.index, path, address, pubkey); err != nil {
		return fmt.Errorf("store address: %w", err)
	}
	sync.Watch(address, path, pubkey)
	return nil
}

// startSync begins the background network sync and shows its connectivity in
// the indicator and the status line.
func (g *gui) startSync(peer string) {
	var err = sync.Start(g.wallet.Net(), g.store, peer, func(s sync.Status) {
		fyne.Do(func() {
			g.signal.SetLevel(s.Bars())
			g.status.SetText(s.String())
			g.showBalance(s.Balance, s.Pending)
			g.rotateIfUsed()
		})
	})
	if err != nil {
		log.Printf("sync failed to start: %v", err)
		g.status.SetText("Sync failed: " + err.Error())
	}
}

// rotateIfUsed moves on to the next address once the displayed one has
// received a payment, confirmed or pending, so every payment goes to a fresh
// address. The new index is saved and the address is watched by the sync.
func (g *gui) rotateIfUsed() {
	for sync.IsUsed(g.addr.Text) {
		var used = g.addr.Text
		g.index++
		if err := g.store.UpdateNextIndex(g.index); err != nil {
			dialog.ShowError(fmt.Errorf("save next address index: %w", err), g.window)
			return
		}
		if err := g.refreshAddress(); err != nil {
			dialog.ShowError(err, g.window)
			return
		}
		log.Printf("address %s received a payment, showing next address %s (index %d)", used, g.addr.Text, g.index)
	}
}

// pendingStyle sets the pending note in small type and the theme's
// placeholder grey, a muted colour that stays readable in both themes.
var pendingStyle = widget.RichTextStyle{
	Alignment: fyne.TextAlignCenter,
	ColorName: theme.ColorNamePlaceHolder,
	SizeName:  theme.SizeNameCaptionText,
}

// usdTextStyle sets the USD balance a step smaller than the balance, in the
// same grey as the pending note.
var usdTextStyle = widget.RichTextStyle{
	ColorName: theme.ColorNamePlaceHolder,
	SizeName:  theme.SizeNameSubHeadingText,
}

// showBalance shows the spendable balance, its worth in US dollars at the
// latest rate and, only while a payment is incoming unconfirmed, the pending
// note next to it. The rows are laid out again, as the texts change width.
func (g *gui) showBalance(confirmed, pending int64) {
	g.total = confirmed + pending
	g.balance.SetText(balanceText(confirmed, pending))
	g.showUSD()
	var note = pendingText(pending)
	g.pending.Segments = []widget.RichTextSegment{&widget.TextSegment{Text: note, Style: pendingStyle}}
	g.pending.Refresh()
	if note == "" {
		g.pending.Hide()
	} else {
		g.pending.Show()
	}
	g.balanceRow.Refresh()
}

// setRate takes a new rate in cents per bitcoin and revalues the balance
// once one is shown.
func (g *gui) setRate(cents int64) {
	g.rate = cents
	if g.balance.Text == "" { return }
	g.showUSD()
	g.balanceRow.Refresh()
}

// showUSD shows the balance in US dollars, or hides the line while no rate
// is known.
func (g *gui) showUSD() {
	if g.rate <= 0 {
		g.usd.Hide()
		return
	}
	g.usd.Segments = []widget.RichTextSegment{&widget.TextSegment{Text: formatUSD(usdValue(g.total, g.rate)), Style: usdTextStyle}}
	g.usd.Refresh()
	g.usd.Show()
}
