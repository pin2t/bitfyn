package gui

import "fmt"
import "time"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/container"
import "fyne.io/fyne/v2/layout"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "bitfyn/internal/sync"

// walletStats sums up the wallet's whole lifetime, unconfirmed transactions
// included as in the balance: the value received from others, the value
// sent to others and the fees paid, all in satoshis, the transactions, the
// coins ever received and those still unspent.
type walletStats struct {
	received int64
	sent     int64
	fees     int64
	txs      int
	coins    int
	unspent  int
}

// computeStats sums the wallet transactions. A transaction the wallet paid
// for, every input its own, counts its fee and the outputs to others as
// sent: a payment between the wallet's own addresses sends nothing. Any
// other transaction counts what it added to the balance as received, or
// what it took as sent.
func computeStats(txs []sync.WalletTx, unspent int) walletStats {
	var s = walletStats{txs: len(txs), unspent: unspent}
	for _, t := range txs {
		for _, o := range t.Outputs {
			if o.Mine { s.coins++ }
		}
		switch {
		case t.Fee > 0:
			s.fees += t.Fee
			s.sent += -t.Net - t.Fee
		case t.Net > 0:
			s.received += t.Net
		default:
			s.sent -= t.Net
		}
	}
	return s
}

// lifetimeText says how long the wallet has existed since the unix time it
// was created, in its two largest units: "1 year 2 months", "3 days",
// "less than a minute".
func lifetimeText(created int64, now time.Time) string {
	if created == 0 { return "—" }
	var age = now.Sub(time.Unix(created, 0))
	for i, u := range timeUnits {
		var n = int(age / u.size)
		if n < 1 { continue }
		var text = countOf(n, u.name)
		if i+1 < len(timeUnits) {
			var next = timeUnits[i+1]
			if rest := int(age % u.size / next.size); rest >= 1 {
				text += " " + countOf(rest, next.name)
			}
		}
		return text
	}
	return "less than a minute"
}

// statCaptionStyle sets a statistic's name small and grey, centred over its
// value.
var statCaptionStyle = widget.RichTextStyle{
	Alignment: fyne.TextAlignCenter,
	ColorName: theme.ColorNamePlaceHolder,
	SizeName:  theme.SizeNameCaptionText,
}

// statsView is the wallet statistics under the Receive and Send buttons:
// a tile for each, its name over its value, the amounts first, then the
// counts and the wallet's age.
type statsView struct {
	content  *fyne.Container
	received *widget.Label
	sent     *widget.Label
	fees     *widget.Label
	txs      *widget.Label
	coins    *widget.Label
	lifetime *widget.Label
}

func newStatsView() *statsView {
	var v = &statsView{}
	var tiles []fyne.CanvasObject
	for _, stat := range []struct {
		name  string
		value **widget.Label
	}{
		{"Total received", &v.received},
		{"Total sent", &v.sent},
		{"Total fees paid", &v.fees},
		{"Transactions", &v.txs},
		{"Coins", &v.coins},
		{"Wallet lifetime", &v.lifetime},
	} {
		*stat.value = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{})
		var caption = widget.NewRichText(&widget.TextSegment{Text: stat.name, Style: statCaptionStyle})
		tiles = append(tiles, container.New(layout.NewCustomPaddedVBoxLayout(0), caption, *stat.value))
	}
	v.content = container.New(statsLayout{}, tiles...)
	return v
}

// show fills the tiles in with the statistics and the wallet's age as of
// now.
func (v *statsView) show(s walletStats, created int64, now time.Time) {
	v.received.SetText(formatAmount(s.received))
	v.sent.SetText(formatAmount(s.sent))
	v.fees.SetText(formatAmount(s.fees))
	v.txs.SetText(fmt.Sprint(s.txs))
	v.coins.SetText(fmt.Sprintf("%d (%d unspent)", s.coins, s.unspent))
	v.lifetime.SetText(lifetimeText(created, now))
	v.content.Refresh()
}

// statsLayout puts the tiles in one row of equal columns when they all fit
// at their widest, and otherwise in two rows, half the tiles in each.
type statsLayout struct{}

// widest is the width of the widest tile and the height of the tallest.
func (statsLayout) widest(objects []fyne.CanvasObject) fyne.Size {
	var size fyne.Size
	for _, o := range objects {
		size = size.Max(o.MinSize())
	}
	return size
}

// columns is how many tiles go in a row of the width.
func (l statsLayout) columns(objects []fyne.CanvasObject, width float32) int {
	var tile = l.widest(objects).Width + theme.Padding()
	if width >= float32(len(objects))*tile { return len(objects) }
	return (len(objects) + 1) / 2
}

// MinSize is the two rows' size, the least the tiles need.
func (l statsLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var tile = l.widest(objects)
	var columns = (len(objects) + 1) / 2
	return fyne.NewSize(float32(columns)*(tile.Width+theme.Padding()), 2*tile.Height+theme.Padding())
}

func (l statsLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var columns = l.columns(objects, size.Width)
	var width = size.Width / float32(columns)
	var height = l.widest(objects).Height
	for i, o := range objects {
		var row, column = i / columns, i % columns
		o.Resize(fyne.NewSize(width, height))
		o.Move(fyne.NewPos(float32(column)*width, float32(row)*(height+theme.Padding())))
	}
}
