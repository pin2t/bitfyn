// Package gui implements the Fyne user interface of the wallet.
package gui

import "errors"
import "fmt"
import "log"
import "path/filepath"
import "time"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/app"
import "fyne.io/fyne/v2/container"
import "fyne.io/fyne/v2/dialog"
import "fyne.io/fyne/v2/layout"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "github.com/btcsuite/btcd/chaincfg"
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
	w.Resize(fyne.NewSize(800, 900))
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
		_ = gui.store.Close()
	})
	w.SetContent(gui.content())
	a.Lifecycle().SetOnStarted(func() { gui.startSync(opts.Peer) })
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
	balance *widget.Label
	pending *widget.RichText
	signal *SignalWidget
	status *widget.Label
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
	}, nil
}

// content builds the window layout: the address QR code in the centre and
// the address text right below it with a clipboard copy icon directly after
// the text, then the wallet balance in large type with a small grey note on
// the pending part below it, both filled in by the sync status.
func (g *gui) content() fyne.CanvasObject {
	g.qr = NewQRWidget("")
	g.addr = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Monospace: true})
	var copyBtn = widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() {
		if g.addr.Text == "" { return }
		fyne.CurrentApp().Clipboard().SetContent(g.addr.Text)
		dialog.ShowInformation("Copied", "Address copied to clipboard", g.window)
	})
	copyBtn.Importance = widget.LowImportance
	var title = widget.NewLabelWithStyle("BitFyn", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	var top = container.NewVBox(title)
	if g.wallet.Net().Net != chaincfg.MainNetParams.Net {
		top.Add(widget.NewLabelWithStyle("net: "+g.net, fyne.TextAlignCenter, fyne.TextStyle{}))
	}
	if err := g.refreshAddress(); err != nil {
		dialog.ShowError(err, g.window)
	}
	g.balance = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	g.balance.SizeName = theme.SizeNameHeadingText
	g.pending = widget.NewRichText(&widget.TextSegment{Style: pendingStyle})
	g.pending.Hide()
	g.signal = NewSignalWidget()
	g.status = widget.NewLabel(sync.Status{}.String())
	var corner = container.NewVBox(
		container.NewHBox(layout.NewSpacer(), g.signal),
		container.NewHBox(layout.NewSpacer(), g.status),
	)
	return container.NewVBox(
		corner,
		top,
		container.NewCenter(g.qr),
		container.NewCenter(container.NewHBox(g.addr, copyBtn)),
		container.NewCenter(g.balance),
		container.NewCenter(g.pending),
	)
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
	sync.Watch(address, pubkey)
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

// showBalance shows the spendable balance and, only while something is
// unconfirmed, the pending note under it.
func (g *gui) showBalance(confirmed, pending int64) {
	g.balance.SetText(balanceText(confirmed, pending))
	var note = pendingText(pending)
	g.pending.Segments = []widget.RichTextSegment{&widget.TextSegment{Text: note, Style: pendingStyle}}
	g.pending.Refresh()
	if note == "" {
		g.pending.Hide()
	} else {
		g.pending.Show()
	}
}
