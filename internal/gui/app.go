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
import "fyne.io/fyne/v2/widget"
import "bitfyn/internal/storage"
import "bitfyn/internal/wallet"

// Options configures the GUI.
type Options struct {
	DataDir string
	Network string
	DBPass  string
}

// Run starts the Fyne application and blocks until the window is closed.
func Run(opts Options) {
	var a = app.NewWithID("bitfyn.wallet")
	var w = a.NewWindow("BitFyn")
	w.Resize(fyne.NewSize(420, 640))
	w.CenterOnScreen()
	var ctrl, err = newGUI(opts, w)
	if err != nil {
		log.Printf("startup failed: %v", err)
		w.SetContent(container.NewVBox(
			widget.NewLabelWithStyle("BitFyn failed to start", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle(err.Error(), fyne.TextAlignCenter, fyne.TextStyle{Monospace: true}),
		))
		w.ShowAndRun()
		return
	}
	w.SetOnClosed(func() { _ = ctrl.store.Close() })
	w.SetContent(ctrl.content())
	w.ShowAndRun()
}

// gui holds the UI state and the wallet/store backend.
type gui struct {
	window fyne.Window
	store  *storage.Store
	wallet *wallet.Wallet
	net    string

	index uint32

	qr     *QRWidget
	addr   *widget.Label
	path   *widget.Label
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

// content builds the window layout: the address QR code in the centre,
// the address text below it, and the action buttons.
func (g *gui) content() fyne.CanvasObject {
	g.qr = NewQRWidget("")
	g.addr = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Monospace: true})
	g.addr.Wrapping = fyne.TextWrapBreak
	g.path = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Monospace: true})
	g.status = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Italic: true})
	var copyBtn = widget.NewButton("Copy Address", func() {
		if g.addr.Text == "" {
			return
		}
		fyne.CurrentApp().Clipboard().SetContent(g.addr.Text)
		g.setStatus("address copied to clipboard")
	})
	var nextBtn = widget.NewButton("New Address", func() {
		if err := g.nextAddress(); err != nil {
			g.setStatus(fmt.Sprintf("error: %v", err))
			dialog.ShowError(err, g.window)
		}
	})
	var title = widget.NewLabelWithStyle("BitFyn", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	var netLabel = widget.NewLabelWithStyle("network: "+g.net, fyne.TextAlignCenter, fyne.TextStyle{})
	if err := g.refreshAddress(); err != nil {
		g.setStatus(fmt.Sprintf("error: %v", err))
	}
	return container.NewBorder(
		container.NewVBox(title, netLabel),
		g.status,
		nil, nil,
		container.NewVBox(
			container.NewCenter(g.qr),
			g.addr,
			g.path,
			container.NewCenter(container.NewHBox(copyBtn, nextBtn)),
		),
	)
}

// refreshAddress derives the current address and updates QR, labels and the
// address table.
func (g *gui) refreshAddress() error {
	var address, path, pubkey, err = g.wallet.DeriveAddress(g.index)
	if err != nil {
		return fmt.Errorf("derive address %d: %w", g.index, err)
	}
	if err := g.qr.SetContent(address); err != nil {
		return fmt.Errorf("encode QR: %w", err)
	}
	g.addr.SetText(address)
	g.path.SetText(path)
	if err := g.store.AddAddress(g.index, path, address, pubkey); err != nil {
		return fmt.Errorf("store address: %w", err)
	}
	g.updateStatus()
	return nil
}

// nextAddress derives the next receive address, persists it and refreshes
// the UI. The database is updated first so the on-disk state stays the
// source of truth.
func (g *gui) nextAddress() error {
	var idx = g.index + 1
	var address, path, pubkey, err = g.wallet.DeriveAddress(idx)
	if err != nil {
		return fmt.Errorf("derive address %d: %w", idx, err)
	}
	if err := g.store.AddAddress(idx, path, address, pubkey); err != nil {
		return fmt.Errorf("store address: %w", err)
	}
	if err := g.store.UpdateNextIndex(idx); err != nil {
		return fmt.Errorf("update next index: %w", err)
	}
	g.index = idx
	if err := g.qr.SetContent(address); err != nil {
		return fmt.Errorf("encode QR: %w", err)
	}
	g.addr.SetText(address)
	g.path.SetText(path)
	g.updateStatus()
	return nil
}

func (g *gui) setStatus(msg string) { g.status.SetText(msg) }

func (g *gui) updateStatus() {
	var n, err = g.store.CountAddresses()
	if err != nil {
		g.setStatus(fmt.Sprintf("error: %v", err))
		return
	}
	g.setStatus(fmt.Sprintf("%d address(es) generated", n))
}
