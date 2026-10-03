package gui

import "testing"
import "time"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/container"
import "fyne.io/fyne/v2/test"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "bitfyn/internal/sync"

// TestTxAmount checks the signed net amounts in sats and BTC.
func TestTxAmount(t *testing.T) {
	var cases = []struct {
		net  int64
		want string
	}{
		{1_000, "+ 1 000 sats"},
		{-1_000, "- 1 000 sats"},
		{150_000_000, "+ 1.5 BTC"},
		{-5_000_000, "- 0.05 BTC"},
		{0, "0 sats"},
	}
	for _, c := range cases {
		if got := txAmount(c.net); got != c.want {
			t.Errorf("txAmount(%d) = %q, want %q", c.net, got, c.want)
		}
	}
}

// testTxs are three transactions: an old large receive, a newer small
// spend and the newest, an unconfirmed receive from an unknown input.
func testTxs(now time.Time, address string) []sync.WalletTx {
	var other = "bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080"
	return []sync.WalletTx{
		{Txid: chainhash.Hash{1}, Time: now.Add(-48 * time.Hour).Unix(), Confirmed: true, Net: 50_000_000,
			Inputs: []sync.TxAddress{{Address: other}}, Outputs: []sync.TxAddress{{Address: address, Mine: true}, {Address: other}}},
		{Txid: chainhash.Hash{2}, Time: now.Add(-3 * time.Hour).Unix(), Confirmed: true, Net: -1_500,
			Inputs: []sync.TxAddress{{Address: address, Mine: true}}, Outputs: []sync.TxAddress{{Address: other}}},
		{Txid: chainhash.Hash{3}, Time: now.Add(-5 * time.Minute).Unix(), Net: 250_000,
			Inputs: []sync.TxAddress{{}}, Outputs: []sync.TxAddress{{Address: address, Mine: true}}},
	}
}

func TestSortTxs(t *testing.T) {
	var now = time.Unix(1_800_000_000, 0)
	var txs = testTxs(now, "a")
	var order = func() []int64 {
		var out []int64
		for _, t := range txs {
			out = append(out, t.Net)
		}
		return out
	}
	var cases = []struct {
		by        coinSort
		ascending bool
		want      []int64
	}{
		{sortByTime, false, []int64{250_000, -1_500, 50_000_000}},
		{sortByTime, true, []int64{50_000_000, -1_500, 250_000}},
		{sortByAmount, false, []int64{50_000_000, 250_000, -1_500}},
		{sortByAmount, true, []int64{-1_500, 250_000, 50_000_000}},
	}
	for _, c := range cases {
		sortTxs(txs, c.by, c.ascending)
		if got := order(); got[0] != c.want[0] || got[1] != c.want[1] || got[2] != c.want[2] {
			t.Errorf("sort %d ascending %v = %v, want %v", c.by, c.ascending, got, c.want)
		}
	}
}

// manyTxs are n confirmed receives a minute apart, the newest first.
func manyTxs(n int, now time.Time, address string) []sync.WalletTx {
	var txs = make([]sync.WalletTx, n)
	for i := range txs {
		txs[i] = sync.WalletTx{Txid: chainhash.Hash{byte(i), byte(i >> 8), 1}, Time: now.Add(-time.Duration(i) * time.Minute).Unix(), Confirmed: true, Net: int64(1_000 + i),
			Inputs: []sync.TxAddress{{Address: "coinbase"}}, Outputs: []sync.TxAddress{{Address: address, Mine: true}}}
	}
	return txs
}

// cardBody returns a card's content under its background, unwrapped from
// the grey theme of an unconfirmed transaction, and whether it was.
func cardBody(card fyne.CanvasObject) (*fyne.Container, bool) {
	var body = card.(*fyne.Container).Objects[1].(*fyne.Container).Objects[0]
	if o, ok := body.(*container.ThemeOverride); ok {
		return o.Content.(*fyne.Container), o.Theme == pendingTheme{}
	}
	return body.(*fyne.Container), false
}

// testTxView opens the Transactions tab of a fresh wallet in a window of
// the size, emptied of the history other tests left in the sync state.
func testTxView(t *testing.T, size fyne.Size) (*gui, *txView) {
	t.Helper()
	var app = test.NewApp()
	t.Cleanup(app.Quit)
	var w = test.NewWindow(nil)
	t.Cleanup(w.Close)
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil {
		t.Fatalf("newGUI: %v", err)
	}
	t.Cleanup(func() { g.store.Close() })
	var tabs = g.tabs(g.content())
	w.SetContent(tabs)
	w.Resize(size)
	tabs.SelectIndex(2)
	if tabs.Selected().Content != g.txView.content {
		t.Fatal("Transactions tab must show the transactions view")
	}
	g.txView.update(nil, time.Now())
	return g, g.txView
}

// TestTxView checks the cards, newest first, with relative times, short
// ids, signed amounts and addresses, the wallet's in bold, an unknown one
// in italics, the unconfirmed one only in grey, sorting from the header
// arrows, the copy icon, the empty state and times refreshed without
// rebuilding.
func TestTxView(t *testing.T) {
	var g, v = testTxView(t, fyne.NewSize(700, 800))
	if !v.empty.Visible() || len(v.list.Objects) != 0 {
		t.Fatal("Transactions tab must start empty with its placeholder")
	}
	var now = time.Now()
	var mine = g.addr.Text
	v.update(testTxs(now, mine), now)
	if v.empty.Visible() || len(v.cards) != 3 {
		t.Fatalf("%d cards, empty shown %v, want 3 cards", len(v.cards), v.empty.Visible())
	}
	var want = []struct {
		when, amount string
		pending      bool
	}{
		{"5 minutes ago", "+ 250 000 sats", true},
		{"3 hours ago", "- 1 500 sats", false},
		{"2 days ago", "+ 0.5 BTC", false},
	}
	for i, w := range want {
		var c = v.cards[i]
		var _, pending = cardBody(v.list.Objects[i])
		if c.when.Text != w.when || c.amount.Text != w.amount || pending != w.pending {
			t.Errorf("card %d: %q %q pending %v, want %q %q pending %v", i, c.when.Text, c.amount.Text, pending, w.when, w.amount, w.pending)
		}
		var id = v.txs[i].Txid.String()
		if len(c.id.Text) >= len(id) || c.id.Text[len(c.id.Text)-addressTail:] != id[len(id)-addressTail:] {
			t.Errorf("card %d: id %q, want %s shortened", i, c.id.Text, id)
		}
	}
	var pendingIn, pendingOut = v.cards[0].inputs[0], v.cards[0].outputs[0]
	if pendingIn.Text != unknownAddress || !pendingIn.TextStyle.Italic || pendingIn.TextStyle.Bold {
		t.Errorf("unknown input %q style %+v, want %q in italics", pendingIn.Text, pendingIn.TextStyle, unknownAddress)
	}
	if !pendingOut.TextStyle.Bold {
		t.Errorf("wallet output %q not bold", pendingOut.Text)
	}
	var received = v.cards[2]
	if len(received.inputs) != 1 || received.inputs[0].TextStyle.Bold || len(received.outputs) != 2 || !received.outputs[0].TextStyle.Bold || received.outputs[1].TextStyle.Bold {
		t.Errorf("receive card addresses: inputs %d, outputs %d, only the wallet's must be bold", len(received.inputs), len(received.outputs))
	}
	var left, right = posOf(received.inputs[0]), posOf(received.outputs[1])
	var card = posOf(v.list.Objects[2])
	var cardWidth = v.list.Objects[2].Size().Width
	if left.X-card.X > cardWidth/4 || right.X+received.outputs[1].Size().Width-card.X < cardWidth*3/4 || left.Y != posOf(received.outputs[0]).Y {
		t.Errorf("input at %v, output at %v, card at %v %v wide: want inputs at the left, outputs at the right", left, right, card, cardWidth)
	}
	test.Tap(v.amountSort.up)
	if v.txs[0].Net != -1_500 || v.cards[0].amount.Text != "- 1 500 sats" {
		t.Errorf("sorted by amount ascending, first card %q", v.cards[0].amount.Text)
	}
	test.Tap(v.timeSort.down)
	var body, _ = cardBody(v.list.Objects[1])
	var top = body.Objects[0].(*fyne.Container)
	var copyIcon = top.Objects[1].(*fyne.Container).Objects[1].(*fyne.Container).Objects[0].(*tapIcon)
	test.Tap(copyIcon)
	if got := fyne.CurrentApp().Clipboard().Content(); got != v.txs[1].Txid.String() {
		t.Errorf("clipboard %q after the copy icon, want %s", got, v.txs[1].Txid)
	}
	var first = v.list.Objects[0]
	v.update(testTxs(now, mine), now.Add(time.Hour))
	if v.list.Objects[0] != first || v.cards[0].when.Text != "1 hour ago" {
		t.Errorf("time refresh rebuilt cards or showed %q", v.cards[0].when.Text)
	}
	v.update(nil, now)
	if !v.empty.Visible() || len(v.list.Objects) != 0 {
		t.Error("an empty history must show the placeholder")
	}
}

// TestTxAddressRows checks that a card lists at most txAddressRows
// addresses on a side, the last row counting the rest.
func TestTxAddressRows(t *testing.T) {
	var _, v = testTxView(t, fyne.NewSize(700, 800))
	var inputs = make([]sync.TxAddress, 10)
	for i := range inputs {
		inputs[i] = sync.TxAddress{Address: "bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080"}
	}
	var now = time.Now()
	v.update([]sync.WalletTx{{Txid: chainhash.Hash{1}, Time: now.Unix(), Confirmed: true, Inputs: inputs, Outputs: inputs[:2]}}, now)
	var c = v.cards[0]
	if len(c.inputs) != txAddressRows || c.inputs[txAddressRows-1].Text != "5 more" || len(c.outputs) != 2 {
		t.Errorf("%d input rows ending %q, %d output rows, want %d ending \"5 more\" and 2", len(c.inputs), c.inputs[len(c.inputs)-1].Text, len(c.outputs), txAddressRows)
	}
}

// TestTxLayout checks the Coins tab's gaps at the sides of the cards, the
// header's Amount ending with the amounts, and cards added a page at a time
// as the list scrolls to its end.
func TestTxLayout(t *testing.T) {
	var g, v = testTxView(t, fyne.NewSize(700, 800))
	var now = time.Now()
	v.update(manyTxs(70, now, g.addr.Text), now)
	if len(v.cards) != txPage {
		t.Fatalf("%d cards at first, want a page of %d", len(v.cards), txPage)
	}
	var top = posOf(v.content)
	var card = v.list.Objects[0]
	var pos = posOf(card)
	if left := pos.X - top.X; left != coinsGap() {
		t.Errorf("card %v from the left, want %v", left, coinsGap())
	}
	if right := top.X + v.content.Size().Width - pos.X - card.Size().Width; right != coinsGap() {
		t.Errorf("card %v from the right, want %v", right, coinsGap())
	}
	var amount = v.cards[0].amount
	var amountEnd = posOf(amount).X + amount.Size().Width
	var headerEnd = posOf(v.amountSort).X + v.amountSort.Size().Width
	if headerEnd < amountEnd-2 || headerEnd > amountEnd+2 {
		t.Errorf("header Amount ends at %v, card amounts at %v", headerEnd, amountEnd)
	}
	var scrollTo = func() {
		v.scroll.Offset.Y = v.scroll.Content.MinSize().Height - v.scroll.Size().Height
		v.scroll.OnScrolled(v.scroll.Offset)
	}
	scrollTo()
	if len(v.cards) != 2*txPage {
		t.Fatalf("%d cards after scrolling to the end, want %d", len(v.cards), 2*txPage)
	}
	scrollTo()
	scrollTo()
	if len(v.cards) != 70 {
		t.Fatalf("%d cards after scrolling to the end again, want all 70", len(v.cards))
	}
	v.update(manyTxs(71, now, g.addr.Text), now)
	if len(v.cards) != 71 {
		t.Errorf("%d cards after a new transaction, want every one still shown", len(v.cards))
	}
	test.Tap(v.amountSort.down)
	if len(v.cards) != txPage || v.scroll.Offset.Y != 0 {
		t.Errorf("%d cards at offset %v after sorting, want a page from the top", len(v.cards), v.scroll.Offset.Y)
	}
}

// posOf is where the object is drawn in the window.
func posOf(o fyne.CanvasObject) fyne.Position {
	return fyne.CurrentApp().Driver().AbsolutePositionForObject(o)
}
