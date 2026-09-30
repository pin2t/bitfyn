package gui

import "testing"
import "time"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/test"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/wallet"

// TestRelativeTime checks the largest whole unit, singular and plural, and
// the unknown and future times.
func TestRelativeTime(t *testing.T) {
	var now = time.Unix(1_800_000_000, 0)
	var cases = []struct {
		age  time.Duration
		want string
	}{
		{0, "just now"},
		{59 * time.Second, "just now"},
		{-time.Hour, "just now"},
		{time.Minute, "1 minute ago"},
		{59 * time.Minute, "59 minutes ago"},
		{time.Hour, "1 hour ago"},
		{23 * time.Hour, "23 hours ago"},
		{24 * time.Hour, "1 day ago"},
		{29 * 24 * time.Hour, "29 days ago"},
		{30 * 24 * time.Hour, "1 month ago"},
		{364 * 24 * time.Hour, "12 months ago"},
		{365 * 24 * time.Hour, "1 year ago"},
		{3 * 365 * 24 * time.Hour, "3 years ago"},
	}
	for _, c := range cases {
		if got := relativeTime(now.Add(-c.age).Unix(), now); got != c.want {
			t.Errorf("relativeTime(%s ago) = %q, want %q", c.age, got, c.want)
		}
	}
	if got := relativeTime(0, now); got != "—" {
		t.Errorf("relativeTime of an unknown time = %q", got)
	}
}

// testCoins are three coins: an old large confirmed one, a newer small
// confirmed one and the newest, unconfirmed.
func testCoins(now time.Time, address string) []wallet.Coin {
	return []wallet.Coin{
		{OutPoint: wire.OutPoint{Hash: chainhash.Hash{1}}, Value: 50_000_000, Address: address, Confirmed: true, Time: now.Add(-48 * time.Hour).Unix()},
		{OutPoint: wire.OutPoint{Hash: chainhash.Hash{2}}, Value: 1_500, Address: address, Confirmed: true, Time: now.Add(-3 * time.Hour).Unix()},
		{OutPoint: wire.OutPoint{Hash: chainhash.Hash{3}}, Value: 250_000, Address: address, Confirmed: false, Time: now.Add(-5 * time.Minute).Unix()},
	}
}

func TestSortCoins(t *testing.T) {
	var now = time.Unix(1_800_000_000, 0)
	var coins = testCoins(now, "a")
	var order = func() []int64 {
		var out []int64
		for _, c := range coins {
			out = append(out, c.Value)
		}
		return out
	}
	var cases = []struct {
		by        coinSort
		ascending bool
		want      []int64
	}{
		{sortByTime, false, []int64{250_000, 1_500, 50_000_000}},
		{sortByTime, true, []int64{50_000_000, 1_500, 250_000}},
		{sortByAmount, false, []int64{50_000_000, 250_000, 1_500}},
		{sortByAmount, true, []int64{1_500, 250_000, 50_000_000}},
	}
	for _, c := range cases {
		sortCoins(coins, c.by, c.ascending)
		if got := order(); got[0] != c.want[0] || got[1] != c.want[1] || got[2] != c.want[2] {
			t.Errorf("sort %d ascending %v = %v, want %v", c.by, c.ascending, got, c.want)
		}
	}
}

// cardLabels returns the texts of a card's time, address and amount labels,
// and whether it shows the unconfirmed hourglass.
func cardLabels(card fyne.CanvasObject) (string, string, string, bool) {
	var row = card.(*fyne.Container).Objects[1].(*fyne.Container).Objects[0].(*fyne.Container)
	var when = row.Objects[0].(*widget.Label).Text
	var address = row.Objects[1].(*fyne.Container).Objects[0].(*widget.Label).Text
	var amount = row.Objects[2].(*fyne.Container).Objects
	var pending = false
	for _, o := range amount {
		if c, ok := o.(*fyne.Container); ok {
			if icon, ok := c.Objects[0].(*tapIcon); ok && icon.icon.Resource == hourglassIcon { pending = true }
		}
	}
	return when, address, amount[len(amount)-1].(*widget.Label).Text, pending
}

// TestCoinsView checks the cards, newest first, with relative times, short
// addresses and amounts, the hourglass on the unconfirmed coin only, sorting
// from the header arrows, the copy icon, the empty state, times refreshed
// without rebuilding, and the Send button.
func TestCoinsView(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var w = test.NewWindow(nil)
	defer w.Close()
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil {
		t.Fatalf("newGUI: %v", err)
	}
	defer g.store.Close()
	var tabs = g.tabs(g.content())
	w.SetContent(tabs)
	w.Resize(fyne.NewSize(700, 800))
	tabs.SelectIndex(1)
	var v = g.coinsView
	if tabs.Selected().Content != v.content || !v.empty.Visible() || len(v.list.Objects) != 0 {
		t.Fatal("Coins tab must start empty with its placeholder")
	}
	var now = time.Now()
	var address = g.addr.Text
	v.update(testCoins(now, address), now)
	if v.empty.Visible() || len(v.list.Objects) != 3 {
		t.Fatalf("%d cards, placeholder visible %v", len(v.list.Objects), v.empty.Visible())
	}
	var want = []struct {
		when, amount string
		pending      bool
	}{
		{"5 minutes ago", "250 000 sats", true},
		{"3 hours ago", "1 500 sats", false},
		{"2 days ago", "0.5 BTC", false},
	}
	for i, c := range want {
		var when, addr, amount, pending = cardLabels(v.list.Objects[i])
		if when != c.when || addr != shortAddress(address) || amount != c.amount || pending != c.pending {
			t.Errorf("card %d = %q %q %q pending %v, want %q %q pending %v", i, when, addr, amount, pending, c.when, c.amount, c.pending)
		}
	}
	var first = v.list.Objects[0]
	v.update(testCoins(now, address), now.Add(time.Hour))
	if v.list.Objects[0] != first {
		t.Fatal("unchanged coins rebuilt the cards")
	}
	if when, _, _, _ := cardLabels(first); when != "1 hour ago" {
		t.Fatalf("time after an hour = %q", when)
	}
	test.Tap(v.amountSort.up)
	if _, _, amount, _ := cardLabels(v.list.Objects[0]); amount != "1 500 sats" || v.by != sortByAmount || !v.ascending {
		t.Fatalf("after sorting by amount ascending the first card is %q", amount)
	}
	test.Tap(v.timeSort.down)
	if _, _, amount, _ := cardLabels(v.list.Objects[0]); amount != "250 000 sats" {
		t.Fatalf("after sorting by time descending the first card is %q", amount)
	}
	var row = v.list.Objects[0].(*fyne.Container).Objects[1].(*fyne.Container).Objects[0].(*fyne.Container)
	var copyIcon = row.Objects[1].(*fyne.Container).Objects[1].(*fyne.Container).Objects[0].(*tapIcon)
	test.Tap(copyIcon)
	if got := app.Clipboard().Content(); got != address {
		t.Fatalf("clipboard %q, want the full address %q", got, address)
	}
	if v.send.Text != "Send" || v.send.Icon != theme.UploadIcon() {
		t.Fatal("Send button must read Send with the upload arrow")
	}
	v.update(nil, now)
	if !v.empty.Visible() || len(v.list.Objects) != 0 {
		t.Fatal("placeholder not back once the coins are spent")
	}
}

// TestCoinsHeaderAlignment checks that the header's Time sits over the card
// times, both starting at the same x.
func TestCoinsHeaderAlignment(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var w = test.NewWindow(nil)
	defer w.Close()
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil {
		t.Fatalf("newGUI: %v", err)
	}
	defer g.store.Close()
	var tabs = g.tabs(g.content())
	w.SetContent(tabs)
	w.Resize(fyne.NewSize(700, 800))
	tabs.SelectIndex(1)
	var now = time.Now()
	g.coinsView.update(testCoins(now, g.addr.Text), now)
	w.Resize(fyne.NewSize(700, 801))
	var header = g.coinsView.content.(*fyne.Container).Objects[1].(*fyne.Container).Objects[0].(*fyne.Container)
	var headerTime = header.Objects[0].(*fyne.Container).Objects[0]
	var cardTime = g.coinsView.times[0]
	var driver = app.Driver()
	var hx = driver.AbsolutePositionForObject(headerTime).X
	var cx = driver.AbsolutePositionForObject(cardTime).X
	if hx != cx {
		t.Fatalf("header Time at x %v, card time at x %v", hx, cx)
	}
}
