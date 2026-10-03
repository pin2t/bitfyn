package gui

import "fmt"
import "testing"
import "time"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/test"
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

// TestLifetimeText checks the two largest units, a whole unit alone, and
// the unknown and newest wallets.
func TestLifetimeText(t *testing.T) {
	var now = time.Unix(1_800_000_000, 0)
	var day = 24 * time.Hour
	var cases = []struct {
		age  time.Duration
		want string
	}{
		{0, "less than a minute"},
		{5 * time.Minute, "5 minutes"},
		{time.Hour + time.Minute, "1 hour 1 minute"},
		{3 * day, "3 days"},
		{3*day + 2*time.Hour, "3 days 2 hours"},
		{45 * day, "1 month 15 days"},
		{365*day + 70*day, "1 year 2 months"},
		{2 * 365 * day, "2 years"},
	}
	for _, c := range cases {
		if got := lifetimeText(now.Add(-c.age).Unix(), now); got != c.want {
			t.Errorf("lifetimeText(%s) = %q, want %q", c.age, got, c.want)
		}
	}
	if got := lifetimeText(0, now); got != "—" {
		t.Errorf("lifetimeText of an unknown creation = %q", got)
	}
}

// TestStatsLayout checks the tiles in one row when they all fit, and in two
// rows of three when they do not.
func TestStatsLayout(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var v = newStatsView()
	v.show(walletStats{received: 12_345_678, sent: 1_234_567, fees: 4_321, txs: 27, coins: 31, unspent: 4}, time.Now().Unix(), time.Now())
	var tiles = v.content.Objects
	var rows = func(width float32) int {
		v.content.Resize(fyne.NewSize(width, v.content.MinSize().Height))
		var ys = map[float32]bool{}
		for _, o := range tiles {
			ys[o.Position().Y] = true
		}
		return len(ys)
	}
	if n := rows(v.content.MinSize().Width); n != 2 || tiles[3].Position().X != 0 || tiles[2].Position().X <= tiles[1].Position().X {
		t.Errorf("narrow: %d rows, the fourth tile at %v, want two rows of three", n, tiles[3].Position())
	}
	if n := rows(3 * v.content.MinSize().Width); n != 1 {
		t.Errorf("wide: %d rows, want one", n)
	}
}

// TestHomeStats checks the statistics on the Home tab: under the Receive
// and Send buttons, in the Coins tab's gaps, and filled in from the wallet
// state.
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
	var stats = driver.AbsolutePositionForObject(g.stats.content)
	var send = driver.AbsolutePositionForObject(g.send)
	if stats.Y < send.Y+g.send.Size().Height {
		t.Errorf("statistics at y %v, over the Send button ending at %v", stats.Y, send.Y+g.send.Size().Height)
	}
	if bottom := stats.Y + g.stats.content.Size().Height; bottom > 800 {
		t.Errorf("statistics end at y %v, below the window", bottom)
	}
	var left = stats.X - driver.AbsolutePositionForObject(home).X
	if left != coinsGap() {
		t.Errorf("statistics %v from the left, want %v", left, coinsGap())
	}
	var now = time.Unix(g.created, 0).Add(3 * 24 * time.Hour)
	g.showWallet(now)
	var s = computeStats(sync.History(), len(sync.Coins()))
	var want = map[*widget.Label]string{
		g.stats.received: formatAmount(s.received),
		g.stats.sent:     formatAmount(s.sent),
		g.stats.fees:     formatAmount(s.fees),
		g.stats.txs:      fmt.Sprint(s.txs),
		g.stats.coins:    fmt.Sprintf("%d (%d unspent)", s.coins, s.unspent),
		g.stats.lifetime: "3 days",
	}
	for label, text := range want {
		if label.Text != text {
			t.Errorf("statistic %q, want %q", label.Text, text)
		}
	}
}
