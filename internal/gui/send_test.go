package gui

import "bytes"
import "strings"
import "testing"
import "fyne.io/fyne/v2/test"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/storage"
import "bitfyn/internal/sync"

// sendGUI builds a regtest GUI whose wallet holds one confirmed coin of
// value on its first receive address.
func sendGUI(t *testing.T, value int64) *gui {
	t.Helper()
	var app = test.NewApp()
	t.Cleanup(app.Quit)
	var w = app.NewWindow("test")
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil {
		t.Fatalf("newGUI: %v", err)
	}
	t.Cleanup(func() { g.store.Close() })
	w.SetContent(g.content())
	var address, _, _, _ = g.wallet.DeriveAddress(0)
	var script, _ = g.wallet.AddressScript(address)
	var tx = wire.NewMsgTx(2)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}}, nil, nil))
	tx.AddTxOut(wire.NewTxOut(value, script))
	var buf bytes.Buffer
	_ = tx.Serialize(&buf)
	if err := g.store.SaveTransaction(storage.Transaction{Txid: tx.TxHash(), Height: 1, BlockHash: chainhash.Hash{2}, Raw: buf.Bytes()}); err != nil {
		t.Fatalf("SaveTransaction: %v", err)
	}
	if err := sync.Init(g.wallet.Net(), g.store); err != nil {
		t.Fatalf("sync.Init: %v", err)
	}
	return g
}

// TestNextChange checks that planning derives a change address without
// storing it, and that a stored unused one is reused.
func TestNextChange(t *testing.T) {
	var g = sendGUI(t, 100_000)
	var first, err = g.nextChange()
	if err != nil || first.stored || first.index != 0 || !strings.Contains(first.path, "/1/0") {
		t.Fatalf("fresh change = %+v, %v", first, err)
	}
	if list, _ := g.store.ChangeAddresses(); len(list) != 0 {
		t.Fatal("planning stored a change address")
	}
	if err := g.store.AddChangeAddress(first.index, first.path, first.address, first.pubkey); err != nil {
		t.Fatalf("AddChangeAddress: %v", err)
	}
	again, err := g.nextChange()
	if err != nil || !again.stored || again.address != first.address {
		t.Fatalf("stored unused change = %+v, %v; want %s reused", again, err, first.address)
	}
}

// TestPlanSend checks the form validation and a planned payment, and that
// sending needs a connected peer.
func TestPlanSend(t *testing.T) {
	var g = sendGUI(t, 100_000)
	var dest, _, _, _ = g.wallet.DeriveAddress(5)
	if _, err := g.planSend(sendRequest{address: "nope", amount: 1000, feeRate: 2}); err == nil {
		t.Error("invalid address accepted")
	}
	if _, err := g.planSend(sendRequest{address: dest, amount: 0, feeRate: 2}); err == nil {
		t.Error("zero amount accepted")
	}
	if _, err := g.planSend(sendRequest{address: dest, amount: 200_000, feeRate: 2}); err == nil {
		t.Error("amount above the balance accepted")
	}
	var p, err = g.planSend(sendRequest{address: dest, amount: 30_000, feeRate: 3})
	if err != nil || len(p.spend.Inputs) != 1 || p.spend.Change != 100_000-30_000-p.spend.Fee || p.spend.Fee <= 0 {
		t.Fatalf("planned = %+v, %v", p.spend, err)
	}
	if text := confirmText(p); !strings.Contains(text, "30 000 sats") || !strings.Contains(text, dest) || !strings.Contains(text, "Spending 1 coin,") {
		t.Errorf("confirmation text = %q", text)
	}
	if _, _, err := g.sendPlanned(p); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("send without peers: %v", err)
	}
}

// TestSendDialog fills in the Send dialog and checks the live summary, the
// Max amount and the unit conversion.
func TestSendDialog(t *testing.T) {
	var g = sendGUI(t, 1_234_567)
	var f = g.openSend()
	if f.summary.Text != "Enter the destination address and the amount." {
		t.Fatalf("empty form summary = %q", f.summary.Text)
	}
	var dest, _, _, _ = g.wallet.DeriveAddress(7)
	f.address.SetText(dest)
	f.amount.SetText("250000")
	if !strings.HasPrefix(f.summary.Text, "Fee: 282 sats (141 vB at 2 sat/vB)") {
		t.Fatalf("summary = %q", f.summary.Text)
	}
	f.unit.SetSelected(unitBTC)
	if f.amount.Text != "0.0025" {
		t.Fatalf("amount in BTC = %q, want 0.0025", f.amount.Text)
	}
	f.fillMax()
	if f.amount.Text != unitText(1_234_567-110*2, unitBTC) {
		t.Fatalf("max = %q", f.amount.Text)
	}
	f.address.SetText("")
	if f.summary.Text != "enter the destination address" {
		t.Fatalf("summary without address = %q", f.summary.Text)
	}
}

// TestReceiveDialog checks that the invoice follows the entered amount and
// that a bad amount is named without changing the invoice.
func TestReceiveDialog(t *testing.T) {
	var g = sendGUI(t, 0)
	var f = g.openReceive()
	f.unit.SetSelected(unitBTC)
	f.amount.SetText("0.0015")
	if f.uri.Text != "bitcoin:"+g.addr.Text+"?amount=0.0015" || f.problem.Text != "" {
		t.Fatalf("invoice = %q, problem %q", f.uri.Text, f.problem.Text)
	}
	f.amount.SetText("0.0015x")
	if f.problem.Text == "" || f.uri.Text != "bitcoin:"+g.addr.Text+"?amount=0.0015" {
		t.Fatalf("bad amount: invoice %q, problem %q", f.uri.Text, f.problem.Text)
	}
}
