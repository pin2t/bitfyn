package gui

import "bytes"
import "reflect"
import "testing"
import "fyne.io/fyne/v2/test"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "github.com/btcsuite/btcd/btcutil"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/storage"
import "bitfyn/internal/sync"

// TestRotateIfUsed checks that a payment to the displayed address moves the
// wallet to the next address, saves the index and leaves an unused address
// in place.
func TestRotateIfUsed(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var w = app.NewWindow("test")
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil {
		t.Fatalf("newGUI: %v", err)
	}
	defer g.store.Close()
	w.SetContent(g.content())
	if err := sync.Init(g.wallet.Net(), g.store); err != nil {
		t.Fatalf("sync.Init: %v", err)
	}
	var first = g.addr.Text
	g.rotateIfUsed()
	if g.index != 0 || g.addr.Text != first {
		t.Fatalf("unused address rotated to %d %s", g.index, g.addr.Text)
	}
	var _, _, pubkey, _ = g.wallet.DeriveAddress(0)
	var script = append([]byte{0x00, 0x14}, btcutil.Hash160(pubkey)...)
	var tx = wire.NewMsgTx(2)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}}, nil, nil))
	tx.AddTxOut(wire.NewTxOut(5000, script))
	var buf bytes.Buffer
	_ = tx.Serialize(&buf)
	if err := g.store.SavePending(storage.PendingTx{Txid: tx.TxHash(), Raw: buf.Bytes(), SeenAt: 1}); err != nil {
		t.Fatalf("SavePending: %v", err)
	}
	if err := sync.Init(g.wallet.Net(), g.store); err != nil {
		t.Fatalf("sync.Init: %v", err)
	}
	g.rotateIfUsed()
	var second, _, _, _ = g.wallet.DeriveAddress(1)
	if g.index != 1 || g.addr.Text != second {
		t.Fatalf("after payment: index %d, address %s; want 1, %s", g.index, g.addr.Text, second)
	}
	if !reflect.DeepEqual(g.qr.modules, NewQRWidget(second).modules) {
		t.Fatal("QR code does not encode the next address")
	}
	var meta, merr = g.store.Meta()
	if merr != nil || meta.NextIndex != 1 {
		t.Fatalf("stored next index = %d, %v; want 1", meta.NextIndex, merr)
	}
}

// TestShowBalance checks the large balance and the small grey pending note,
// which is shown only while a payment is incoming unconfirmed.
func TestShowBalance(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var w = app.NewWindow("test")
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil {
		t.Fatalf("newGUI: %v", err)
	}
	defer g.store.Close()
	w.SetContent(g.content())
	if g.balance.SizeName != theme.SizeNameHeadingText {
		t.Fatal("balance must be heading size")
	}
	g.showBalance(1_000_000, 250_000)
	var note = g.pending.Segments[0].(*widget.TextSegment)
	if g.balance.Text != "1 250 000 sats" || note.Text != "(250 000 sats pending)" || !g.pending.Visible() {
		t.Fatalf("with pending: %q, %q, visible %v", g.balance.Text, note.Text, g.pending.Visible())
	}
	if note.Style.SizeName != theme.SizeNameCaptionText || note.Style.ColorName != theme.ColorNamePlaceHolder {
		t.Fatal("pending note must be caption size in the placeholder grey")
	}
	g.showBalance(1_250_000, 0)
	if g.balance.Text != "1 250 000 sats" || g.pending.Visible() {
		t.Fatalf("without pending: %q, note visible %v", g.balance.Text, g.pending.Visible())
	}
	g.showBalance(1_250_000, -50_000)
	if g.balance.Text != "1 200 000 sats" || g.pending.Visible() {
		t.Fatalf("pending spend: %q, note visible %v", g.balance.Text, g.pending.Visible())
	}
}
