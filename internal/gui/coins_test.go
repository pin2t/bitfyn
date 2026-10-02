package gui

import "strings"
import "testing"
import "time"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/container"
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

// cardCells returns a card's time, address and amount cells, unwrapped from
// the grey theme of an unconfirmed coin, and whether they were.
func cardCells(card fyne.CanvasObject) ([]fyne.CanvasObject, bool) {
	var row = card.(*fyne.Container).Objects[1].(*fyne.Container).Objects[0].(*fyne.Container)
	var cells = append([]fyne.CanvasObject(nil), row.Objects[1:]...)
	var pending = false
	for i, cell := range cells {
		if o, ok := cell.(*container.ThemeOverride); ok {
			cells[i] = o.Content
			pending = o.Theme == pendingTheme{}
		}
	}
	return cells, pending
}

// cardLabels returns the texts of a card's time, address and amount labels,
// and whether it is shown as unconfirmed.
func cardLabels(card fyne.CanvasObject) (string, string, string, bool) {
	var cells, pending = cardCells(card)
	var when = cells[0].(*widget.Label).Text
	var address = cells[1].(*fyne.Container).Objects[0].(*widget.Label).Text
	var amount = cells[2].(*fyne.Container).Objects
	return when, address, amount[len(amount)-1].(*widget.Label).Text, pending
}

// TestCoinsView checks the cards, newest first, with relative times, short
// addresses and amounts, the unconfirmed coin only in grey, sorting
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
		if when != c.when || !strings.HasSuffix(addr, address[len(address)-addressTail:]) || amount != c.amount || pending != c.pending {
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
	var cells, _ = cardCells(v.list.Objects[0])
	var copyIcon = cells[1].(*fyne.Container).Objects[1].(*fyne.Container).Objects[0].(*tapIcon)
	test.Tap(copyIcon)
	if got := app.Clipboard().Content(); got != address {
		t.Fatalf("clipboard %q, want the full address %q", got, address)
	}
	if v.send.Text != "Send" || v.send.Icon != theme.UploadIcon() {
		t.Fatal("Send button must read Send with the upload arrow")
	}
	if v.merge.Text != "Merge" || !v.merge.Disabled() || v.split.Text != "Split" || !v.split.Disabled() {
		t.Fatal("Merge and Split buttons must be there, disabled until they work")
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
	var header = g.coinsView.content.Objects[0].(*fyne.Container).Objects[0].(*fyne.Container).Objects[0].(*fyne.Container)
	var headerTime = header.Objects[1].(*fyne.Container).Objects[0]
	var cardTime = g.coinsView.times[0]
	var driver = app.Driver()
	var hx = driver.AbsolutePositionForObject(headerTime).X
	var cx = driver.AbsolutePositionForObject(cardTime).X
	if hx != cx {
		t.Fatalf("header Time at x %v, card time at x %v", hx, cx)
	}
}

// manyCoins are n confirmed coins a minute apart.
func manyCoins(n int, now time.Time, address string) []wallet.Coin {
	var coins = make([]wallet.Coin, n)
	for i := range coins {
		coins[i] = wallet.Coin{OutPoint: wire.OutPoint{Hash: chainhash.Hash{byte(i + 1)}}, Value: int64(1_000 + i), Address: address, Confirmed: true, Time: now.Add(-time.Duration(i) * time.Minute).Unix()}
	}
	return coins
}

// TestCoinsLayout checks the gaps at the sides of the cards, Merge and Split
// left of Send, the buttons right under a short list, and a long list
// scrolling with the buttons kept a gap above the window's bottom.
func TestCoinsLayout(t *testing.T) {
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
	w.Resize(fyne.NewSize(700, 600))
	tabs.SelectIndex(1)
	var v = g.coinsView
	var driver = app.Driver()
	var now = time.Now()
	var check = func(n int) {
		v.update(manyCoins(n, now, g.addr.Text), now)
		var top = driver.AbsolutePositionForObject(v.content)
		var size = v.content.Size()
		var card = v.list.Objects[0]
		var cardPos = driver.AbsolutePositionForObject(card)
		if left := cardPos.X - top.X; left < coinsGap() {
			t.Errorf("%d coins: card %v from the left, want at least %v", n, left, coinsGap())
		}
		if right := top.X + size.Width - cardPos.X - card.Size().Width; right < coinsGap() {
			t.Errorf("%d coins: card %v from the right, want at least %v", n, right, coinsGap())
		}
		var merge, split, send = driver.AbsolutePositionForObject(v.merge), driver.AbsolutePositionForObject(v.split), driver.AbsolutePositionForObject(v.send)
		if !(merge.X < split.X && split.X < send.X) || merge.Y != send.Y || split.Y != send.Y {
			t.Errorf("%d coins: Merge at %v, Split at %v, Send at %v, want one row in that order", n, merge, split, send)
		}
		var list = v.content.Objects[1]
		var listBottom = driver.AbsolutePositionForObject(list).Y + list.Size().Height
		var mergeLeft, sendRight = merge.X - top.X, top.X + size.Width - send.X - v.send.Size().Width
		if mergeLeft != cardPos.X-top.X || sendRight != top.X+size.Width-cardPos.X-card.Size().Width {
			t.Errorf("%d coins: Merge %v from the left, Send %v from the right, want the cards' gaps", n, mergeLeft, sendRight)
		}
		if send.Y < listBottom || send.Y > listBottom+2*theme.Padding() {
			t.Errorf("%d coins: Send at y %v, list ends at %v", n, send.Y, listBottom)
		}
		if gap := top.Y + size.Height - send.Y - v.send.Size().Height; gap < coinsGap() {
			t.Errorf("%d coins: Send %v above the bottom, want at least %v", n, gap, coinsGap())
		}
	}
	check(2)
	if list := v.content.Objects[1].Size().Height; list != v.list.MinSize().Height {
		t.Errorf("short list %v high, want its cards' %v", list, v.list.MinSize().Height)
	}
	check(40)
	if list := v.content.Objects[1].Size().Height; list >= v.list.MinSize().Height {
		t.Errorf("long list %v high, not scrolling its cards' %v", list, v.list.MinSize().Height)
	}
}

// TestCoinsSelection checks the check boxes: selecting cards one by one with
// the header box going partly then fully checked, the header box selecting
// and clearing all, the selection kept across sorting, and spent coins
// leaving it.
func TestCoinsSelection(t *testing.T) {
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
	tabs.SelectIndex(1)
	var v = g.coinsView
	if !v.selectAll.Disabled() {
		t.Fatal("select all must be disabled without coins")
	}
	var now = time.Now()
	var coins = testCoins(now, g.addr.Text)
	v.update(coins, now)
	if v.selectAll.Disabled() || v.selectAll.Checked || v.selectAll.Partial || len(v.selection()) != 0 {
		t.Fatal("coins must start unselected")
	}
	test.Tap(v.checks[0])
	if !v.selectAll.Partial || v.selectAll.Checked || len(v.selection()) != 1 || v.selection()[0].OutPoint != v.coins[0].OutPoint {
		t.Fatalf("one of three selected: partial %v checked %v, %d selected", v.selectAll.Partial, v.selectAll.Checked, len(v.selection()))
	}
	test.Tap(v.checks[1])
	test.Tap(v.checks[2])
	if !v.selectAll.Checked || v.selectAll.Partial || len(v.selection()) != 3 {
		t.Fatal("header box not checked with every coin selected")
	}
	test.Tap(v.selectAll)
	if v.selectAll.Checked || len(v.selection()) != 0 || v.checks[0].Checked {
		t.Fatal("header box did not clear the selection")
	}
	test.Tap(v.selectAll)
	if len(v.selection()) != 3 || !v.checks[2].Checked {
		t.Fatal("header box did not select every coin")
	}
	test.Tap(v.checks[1])
	var kept = v.coins[0].OutPoint
	test.Tap(v.amountSort.up)
	if len(v.selection()) != 2 {
		t.Fatalf("sorting changed the selection to %d coins", len(v.selection()))
	}
	for i, c := range v.coins {
		var want = false
		for _, s := range v.selection() {
			if s.OutPoint == c.OutPoint { want = true }
		}
		if v.checks[i].Checked != want {
			t.Fatalf("card %d check box %v, selected %v", i, v.checks[i].Checked, want)
		}
	}
	var left []wallet.Coin
	for _, c := range coins {
		if c.OutPoint != kept { left = append(left, c) }
	}
	v.update(left, now)
	if len(v.selection()) != 1 || !v.selectAll.Partial {
		t.Fatalf("spent coin still selected: %d selected", len(v.selection()))
	}
}

// TestFitAddress checks the address kept whole when it fits, else shortened
// to the longest start that fits with its last characters, down to the
// ellipsis and the last characters alone.
func TestFitAddress(t *testing.T) {
	const address = "bc1q0r80j388qfu32q67u6zjx3lhm9wsf07gryt4xl"
	var cases = []struct {
		room int
		want string
	}{
		{100, address},
		{len(address), address},
		{20, "bc1q0r80j388q…ryt4xl"},
		{12, "bc1q0…ryt4xl"},
		{11, "…ryt4xl"},
		{3, "…ryt4xl"},
	}
	for _, c := range cases {
		var got = fitAddress(address, func(s string) bool { return len([]rune(s)) <= c.room })
		if got != c.want {
			t.Errorf("room %d: %q, want %q", c.room, got, c.want)
		}
	}
}

// TestCoinsAddressFit checks that at the window's width each card's address
// and copy icon end before its amount, a card with a long amount showing less
// of the address, and that the copy icon still copies the whole address.
func TestCoinsAddressFit(t *testing.T) {
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
	w.Resize(fyne.NewSize(600, 800))
	tabs.SelectIndex(1)
	var v = g.coinsView
	var now = time.Now()
	const address = "bc1q0r80j388qfu32q67u6zjx3lhm9wsf07gryt4xl"
	var coins = testCoins(now, address)
	coins[0].Value = 4_999_999
	coins[1].Value = 1
	v.update(coins, now)
	w.Resize(fyne.NewSize(600, 801))
	var driver = app.Driver()
	var shown = map[string]string{}
	for _, card := range v.list.Objects {
		var cells, _ = cardCells(card)
		var icon = cells[1].(*fyne.Container).Objects[1]
		var amount = cells[2].(*fyne.Container).Objects[1].(*widget.Label)
		var iconEnd = driver.AbsolutePositionForObject(icon).X + icon.Size().Width
		if amountStart := driver.AbsolutePositionForObject(amount).X; iconEnd > amountStart {
			t.Errorf("%s: copy icon ends at %v, after the amount starting at %v", amount.Text, iconEnd, amountStart)
		}
		var _, addr, _, _ = cardLabels(card)
		shown[amount.Text] = addr
	}
	var long, short = shown["4 999 999 sats"], shown["1 sats"]
	if !strings.HasSuffix(long, "ryt4xl") || len(long) >= len(short) {
		t.Errorf("long amount shows %q, short amount %q", long, short)
	}
}
