package gui

import "fmt"
import "testing"
import "time"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/test"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "bitfyn/internal/sync"

// TestComputeStats checks a receive, a payment with change, a payment
// between the wallet's own addresses, and a spend of wallet coins the wallet
// did not pay for alone.
func TestComputeStats(t *testing.T) {
	var mine, other = sync.TxAddress{Address: "m", Mine: true}, sync.TxAddress{Address: "o"}
	var txs = []sync.WalletTx{
		{Net: 100_000, Inputs: []sync.TxAddress{other}, Outputs: []sync.TxAddress{mine, other}},
		{Net: -30_500, Fee: 500, Inputs: []sync.TxAddress{mine}, Outputs: []sync.TxAddress{other, mine}},
		{Net: -300, Fee: 300, Inputs: []sync.TxAddress{mine, mine}, Outputs: []sync.TxAddress{mine}},
		{Net: -2_000, Inputs: []sync.TxAddress{mine, other}, Outputs: []sync.TxAddress{other}},
	}
	var got = computeStats(txs, 2)
	var want = walletStats{received: 100_000, sent: 32_000, fees: 800, txs: 4, coins: 3, unspent: 2}
	if got != want {
		t.Errorf("computeStats = %+v, want %+v", got, want)
	}
	if empty := computeStats(nil, 0); empty != (walletStats{}) {
		t.Errorf("computeStats of no transactions = %+v", empty)
	}
}

// TestLifetimeText checks the two largest units, a whole unit alone, the
// largest unit only, and the unknown and newest wallets.
func TestLifetimeText(t *testing.T) {
	var now = time.Unix(1_800_000_000, 0)
	var day = 24 * time.Hour
	var cases = []struct {
		age  time.Duration
		want string
		one  string
	}{
		{0, "less than a minute", "less than a minute"},
		{5 * time.Minute, "5 minutes", "5 minutes"},
		{time.Hour + time.Minute, "1 hour 1 minute", "1 hour"},
		{3 * day, "3 days", "3 days"},
		{3*day + 2*time.Hour, "3 days 2 hours", "3 days"},
		{45 * day, "1 month 15 days", "1 month"},
		{365*day + 70*day, "1 year 2 months", "1 year"},
		{2 * 365 * day, "2 years", "2 years"},
	}
	for _, c := range cases {
		var created = now.Add(-c.age).Unix()
		if got, one := lifetimeText(created, now, 2), lifetimeText(created, now, 1); got != c.want || one != c.one {
			t.Errorf("lifetimeText(%s) = %q and %q, want %q and %q", c.age, got, one, c.want, c.one)
		}
	}
	if got := lifetimeText(0, now, 2); got != "—" {
		t.Errorf("lifetimeText of an unknown creation = %q", got)
	}
}

// TestStatsLayout checks the fields in two columns, the amounts down the
// left and the counts down the right, rows lined up, each name at the left
// and value at the right of its field, the US dollar values close under
// the amounts, shown only with a known rate, and the lifetime in one unit
// when two do not fit.
func TestStatsLayout(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var v = newStatsView()
	var w = test.NewWindow(v.object())
	defer w.Close()
	var s = walletStats{received: 12_345_678, sent: 1_234_567, fees: 4_321, txs: 27, coins: 31, unspent: 4}
	v.show(s, 0, time.Now().Unix(), time.Now())
	if v.received.usd.Visible() {
		t.Error("US dollar values shown without a rate")
	}
	v.show(s, 5_000_000, time.Now().Unix(), time.Now())
	var fields = v.content.Objects
	v.content.Resize(fyne.NewSize(v.content.MinSize().Width+100, v.content.MinSize().Height))
	for i := 0; i < 3; i++ {
		var left, right = fields[i].Position(), fields[i+3].Position()
		var end = right.X + fields[i+3].Size().Width
		if left.X != 0 || right.X < fields[i].Size().Width+statsColumnGap() || end != v.content.Size().Width || left.Y != right.Y {
			t.Errorf("row %d: fields at %v and %v, want side by side in two columns", i, left, right)
		}
	}
	var f = v.received
	var name, value, usd = f.name.Position(), f.value.Position(), f.usd.Position()
	var width = fields[0].Size().Width + theme.InnerPadding()
	if name.X != -theme.InnerPadding() || name.Y != value.Y || value.X+f.value.Size().Width != width {
		t.Errorf("name at %v, value at %v ending at %v, want one line across the field %v wide", name, value, value.X+f.value.Size().Width, width)
	}
	if !f.usd.Visible() || f.usd.String() != "6 172.84 USD" || usd.X+f.usd.Size().Width != width {
		t.Errorf("USD %q shown %v ending at %v, want 6 172.84 USD at the right", f.usd.String(), f.usd.Visible(), usd.X+f.usd.Size().Width)
	}
	if valueBottom := value.Y + f.value.Size().Height; usd.Y >= valueBottom || usd.Y <= value.Y {
		t.Errorf("USD at y %v, value from %v to %v, want close under it", usd.Y, value.Y, valueBottom)
	}
	if v.txs.usd != nil {
		t.Error("a count must have no US dollar value")
	}
	v.setRate(10_000_000)
	if f.usd.String() != "12 345.68 USD" {
		t.Errorf("USD after a new rate %q, want 12 345.68 USD", f.usd.String())
	}
	var now = time.Now()
	v.show(s, 5_000_000, now.Add(-359*24*time.Hour).Unix(), now)
	v.content.Resize(v.content.MinSize())
	if got := v.lifetime.value.String(); got != "11 months" {
		t.Errorf("lifetime %q when narrow, want 11 months", got)
	}
	v.content.Resize(fyne.NewSize(v.content.MinSize().Width+200, v.content.MinSize().Height))
	if got := v.lifetime.value.String(); got != "11 months 29 days" {
		t.Errorf("lifetime %q when wide, want 11 months 29 days", got)
	}
}

// TestHomeStats checks the statistics on the Home tab: at the bottom, in
// the Coins tab's gaps, under the Receive and Send buttons, in larger text,
// and filled in from the wallet state.
func TestHomeStats(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var w = test.NewWindow(nil)
	defer w.Close()
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil {
		t.Fatalf("newGUI: %v", err)
	}
	defer g.store.Close()
	if g.created == 0 {
		t.Fatal("the wallet creation time is not loaded")
	}
	var home = g.content()
	w.SetContent(g.tabs(home))
	w.Resize(fyne.NewSize(600, 800))
	var driver = app.Driver()
	var top = driver.AbsolutePositionForObject(home)
	var stats = driver.AbsolutePositionForObject(g.stats.content)
	var send = driver.AbsolutePositionForObject(g.send)
	if stats.Y < send.Y+g.send.Size().Height {
		t.Errorf("statistics at y %v, over the Send button ending at %v", stats.Y, send.Y+g.send.Size().Height)
	}
	if gap := top.Y + home.Size().Height - stats.Y - g.stats.content.Size().Height; gap != coinsGap() {
		t.Errorf("statistics %v above the bottom, want %v", gap, coinsGap())
	}
	if left := stats.X - top.X; left != coinsGap() {
		t.Errorf("statistics %v from the left, want %v", left, coinsGap())
	}
	if size := (statsTheme{}).Size(statTextSize); size <= theme.TextSize() {
		t.Errorf("statistics text size %v, want larger than %v", size, theme.TextSize())
	}
	var now = time.Unix(g.created, 0).Add(3 * 24 * time.Hour)
	g.showWallet(now)
	var s = computeStats(sync.History(), len(sync.Coins()))
	var want = map[*widget.RichText]string{
		g.stats.received.value: formatAmount(s.received),
		g.stats.sent.value:     formatAmount(s.sent),
		g.stats.fees.value:     formatAmount(s.fees),
		g.stats.txs.value:      fmt.Sprint(s.txs),
		g.stats.coins.value:    fmt.Sprintf("%d (%d unspent)", s.coins, s.unspent),
		g.stats.lifetime.value: "3 days",
	}
	for value, text := range want {
		if value.String() != text {
			t.Errorf("statistic %q, want %q", value.String(), text)
		}
	}
}
