package gui

import "bytes"
import "reflect"
import "testing"
import "fyne.io/fyne/v2"
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
	var usd = g.usd.Segments[0].(*widget.TextSegment)
	if !g.usd.Visible() || usd.Text != "1 006.49 USD" {
		t.Fatalf("USD balance %q, visible %v; want 1 006.49 USD at the seeded rate", usd.Text, g.usd.Visible())
	}
	if usd.Style.SizeName != theme.SizeNameSubHeadingText || usd.Style.ColorName != theme.ColorNamePlaceHolder {
		t.Fatal("USD balance must be subheading size in the placeholder grey")
	}
	g.setRate(10_000_000)
	if usd = g.usd.Segments[0].(*widget.TextSegment); usd.Text != "1 200.00 USD" {
		t.Fatalf("USD balance after a new rate = %q, want 1 200.00 USD", usd.Text)
	}
}

// TestActions checks the Receive and Send buttons: labels, icons, and
// Receive laid out left of Send, both under the balance.
func TestActions(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var w = test.NewWindow(nil)
	w.Resize(fyne.NewSize(700, 800))
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil {
		t.Fatalf("newGUI: %v", err)
	}
	defer g.store.Close()
	w.SetContent(g.content())
	if g.receive.Text != "Receive" || g.receive.Icon != theme.DownloadIcon() {
		t.Fatalf("receive button = %q with %v", g.receive.Text, g.receive.Icon)
	}
	if g.send.Text != "Send" || g.send.Icon != theme.UploadIcon() {
		t.Fatalf("send button = %q with %v", g.send.Text, g.send.Icon)
	}
	var receiveAt = fyne.CurrentApp().Driver().AbsolutePositionForObject(g.receive)
	var sendAt = fyne.CurrentApp().Driver().AbsolutePositionForObject(g.send)
	var balanceAt = fyne.CurrentApp().Driver().AbsolutePositionForObject(g.balance)
	if receiveAt.X >= sendAt.X || receiveAt.Y != sendAt.Y {
		t.Fatalf("receive at %v, send at %v; want receive left of send on one row", receiveAt, sendAt)
	}
	if receiveAt.Y <= balanceAt.Y {
		t.Fatalf("buttons at y %v not below the balance at y %v", receiveAt.Y, balanceAt.Y)
	}
}

// TestSignalAlignment checks that the highest connectivity bar ends where the
// status text under it ends, and that the text starts one padding under the
// bars, on the line of the network name.
func TestSignalAlignment(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var w = test.NewWindow(nil)
	defer w.Close()
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil {
		t.Fatalf("newGUI: %v", err)
	}
	defer g.store.Close()
	var content = g.content()
	g.status.SetText(sync.Status{Peers: 3}.String())
	w.SetContent(content)
	w.Resize(fyne.NewSize(700, 800))
	var driver = app.Driver()
	var barsEnd = driver.AbsolutePositionForObject(g.signal).X + g.signal.Size().Width
	var textEnd = driver.AbsolutePositionForObject(g.status).X + g.status.Size().Width - theme.InnerPadding()
	if barsEnd != textEnd {
		t.Fatalf("highest bar ends at %v, status text at %v", barsEnd, textEnd)
	}
	var barsBottom = driver.AbsolutePositionForObject(g.signal).Y + g.signal.Size().Height
	var status = driver.AbsolutePositionForObject(g.status)
	if textTop := status.Y + theme.InnerPadding(); textTop != barsBottom+theme.Padding() {
		t.Fatalf("status text starts at %v, bars end at %v, want one padding apart", textTop, barsBottom)
	}
	var network = driver.AbsolutePositionForObject(content.(*fyne.Container).Objects[0].(*fyne.Container).Objects[2])
	if network.Y != status.Y {
		t.Fatalf("network name at y %v, status at y %v", network.Y, status.Y)
	}
}
