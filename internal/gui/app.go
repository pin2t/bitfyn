// Package gui implements the Fyne user interface of the wallet.
package gui

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"spvbit/internal/storage"
	"spvbit/internal/wallet"
)

// Options configures the GUI.
type Options struct {
	DataDir string
	Network string
	DBPass  string
}

// Run starts the Fyne application and blocks until the window is closed.
func Run(opts Options) {
	a := app.NewWithID("io.spvbit.wallet")
	w := a.NewWindow("SPVBit")
	w.Resize(fyne.NewSize(420, 640))
	w.CenterOnScreen()

	ctrl, err := newController(opts, w)
	if err != nil {
		log.Printf("startup failed: %v", err)
		w.SetContent(container.NewVBox(
			widget.NewLabelWithStyle("SPVBit failed to start", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle(err.Error(), fyne.TextAlignCenter, fyne.TextStyle{Monospace: true}),
		))
		w.ShowAndRun()
		return
	}

	w.SetOnClosed(func() { _ = ctrl.store.Close() })
	w.SetContent(ctrl.content())
	w.ShowAndRun()
}

// controller holds the UI state and the wallet/store backend.
type controller struct {
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

// newController opens the database, creating the wallet on first run, and
// prepares the UI state.
func newController(opts Options, w fyne.Window) (*controller, error) {
	net, err := wallet.ParamsForNetwork(opts.Network)
	if err != nil {
		return nil, err
	}

	store, err := storage.Open(filepath.Join(opts.DataDir, "spvbit.db"), opts.DBPass)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*controller, error) {
		_ = store.Close()
		return nil, err
	}

	meta, err := store.Meta()
	var wl *wallet.Wallet
	switch {
	case errors.Is(err, storage.ErrNoWallet):
		mnemonic, err := wallet.NewMnemonic(128)
		if err != nil {
			return fail(fmt.Errorf("generate mnemonic: %w", err))
		}
		wl, err = wallet.New(mnemonic, "", net)
		if err != nil {
			return fail(err)
		}
		xpub, err := wl.AccountXPub()
		if err != nil {
			return fail(err)
		}
		if err := store.SaveMeta(mnemonic, xpub, opts.Network, time.Now().Unix()); err != nil {
			return fail(fmt.Errorf("save wallet: %w", err))
		}
		meta, err = store.Meta()
		if err != nil {
			return fail(err)
		}
	case err != nil:
		return fail(err)
	default:
		if meta.Network != opts.Network {
			return fail(fmt.Errorf("wallet database is for network %q, not %q", meta.Network, opts.Network))
		}
		wl, err = wallet.New(meta.Mnemonic, "", net)
		if err != nil {
			return fail(err)
		}
	}

	return &controller{
		window: w,
		store:  store,
		wallet: wl,
		net:    meta.Network,
		index:  meta.NextIndex,
	}, nil
}

// content builds the window layout: the address QR code in the centre,
// the address text below it, and the action buttons.
func (c *controller) content() fyne.CanvasObject {
	c.qr = NewQRWidget("")
	c.addr = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Monospace: true})
	c.addr.Wrapping = fyne.TextWrapBreak
	c.path = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Monospace: true})
	c.status = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Italic: true})

	copyBtn := widget.NewButton("Copy Address", func() {
		if c.addr.Text == "" {
			return
		}
		fyne.CurrentApp().Clipboard().SetContent(c.addr.Text)
		c.setStatus("address copied to clipboard")
	})
	nextBtn := widget.NewButton("New Address", func() {
		if err := c.nextAddress(); err != nil {
			c.setStatus(fmt.Sprintf("error: %v", err))
			dialog.ShowError(err, c.window)
		}
	})

	title := widget.NewLabelWithStyle("SPVBit", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	netLabel := widget.NewLabelWithStyle("network: "+c.net, fyne.TextAlignCenter, fyne.TextStyle{})

	if err := c.refreshAddress(); err != nil {
		c.setStatus(fmt.Sprintf("error: %v", err))
	}

	return container.NewBorder(
		container.NewVBox(title, netLabel),
		c.status,
		nil, nil,
		container.NewVBox(
			container.NewCenter(c.qr),
			c.addr,
			c.path,
			container.NewCenter(container.NewHBox(copyBtn, nextBtn)),
		),
	)
}

// refreshAddress derives the current address and updates QR, labels and the
// address table.
func (c *controller) refreshAddress() error {
	address, path, pubkey, err := c.wallet.DeriveAddress(c.index)
	if err != nil {
		return fmt.Errorf("derive address %d: %w", c.index, err)
	}
	if err := c.qr.SetContent(address); err != nil {
		return fmt.Errorf("encode QR: %w", err)
	}
	c.addr.SetText(address)
	c.path.SetText(path)
	if err := c.store.AddAddress(c.index, path, address, pubkey); err != nil {
		return fmt.Errorf("store address: %w", err)
	}
	c.updateStatus()
	return nil
}

// nextAddress derives the next receive address, persists it and refreshes
// the UI. The database is updated first so the on-disk state stays the
// source of truth.
func (c *controller) nextAddress() error {
	idx := c.index + 1
	address, path, pubkey, err := c.wallet.DeriveAddress(idx)
	if err != nil {
		return fmt.Errorf("derive address %d: %w", idx, err)
	}
	if err := c.store.AddAddress(idx, path, address, pubkey); err != nil {
		return fmt.Errorf("store address: %w", err)
	}
	if err := c.store.UpdateNextIndex(idx); err != nil {
		return fmt.Errorf("update next index: %w", err)
	}
	c.index = idx

	if err := c.qr.SetContent(address); err != nil {
		return fmt.Errorf("encode QR: %w", err)
	}
	c.addr.SetText(address)
	c.path.SetText(path)
	c.updateStatus()
	return nil
}

func (c *controller) setStatus(msg string) { c.status.SetText(msg) }

func (c *controller) updateStatus() {
	n, err := c.store.CountAddresses()
	if err != nil {
		c.setStatus(fmt.Sprintf("error: %v", err))
		return
	}
	c.setStatus(fmt.Sprintf("%d address(es) generated", n))
}
