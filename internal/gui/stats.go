package gui

import "fmt"
import "image/color"
import "time"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/container"
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
// was created, in at most that many of its largest units: "1 year 2 months"
// or "1 year", "3 days", "less than a minute".
func lifetimeText(created int64, now time.Time, units int) string {
	if created == 0 { return "—" }
	var age = now.Sub(time.Unix(created, 0))
	for i, u := range timeUnits {
		var n = int(age / u.size)
		if n < 1 { continue }
		var text = countOf(n, u.name)
		if units > 1 && i+1 < len(timeUnits) {
			var next = timeUnits[i+1]
			if rest := int(age % u.size / next.size); rest >= 1 {
				text += " " + countOf(rest, next.name)
			}
		}
		return text
	}
	return "less than a minute"
}

// Sizes of the statistics text, set by statsTheme: the field names and
// values a step larger than body text, the US dollar values under the
// amounts smaller.
const statTextSize fyne.ThemeSizeName = "bitfynStatText"
const statUSDSize fyne.ThemeSizeName = "bitfynStatUSD"

// statsTheme gives the statistics their text sizes, leaving everything else
// to the app theme.
type statsTheme struct{}

func (statsTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	return fyne.CurrentApp().Settings().Theme().Color(name, variant)
}

func (statsTheme) Font(style fyne.TextStyle) fyne.Resource {
	return fyne.CurrentApp().Settings().Theme().Font(style)
}

func (statsTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return fyne.CurrentApp().Settings().Theme().Icon(name)
}

func (statsTheme) Size(name fyne.ThemeSizeName) float32 {
	var base = fyne.CurrentApp().Settings().Theme()
	switch name {
	case statTextSize:
		return base.Size(theme.SizeNameText) + 2
	case statUSDSize:
		return base.Size(theme.SizeNameText) - 1
	}
	return base.Size(name)
}

// statTextWidth is the width of a field name or value showing the text,
// with its padding.
func statTextWidth(text string) float32 {
	var app = fyne.CurrentApp()
	if app == nil { return 0 }
	var measured, _ = app.Driver().RenderedTextSize(text, (statsTheme{}).Size(statTextSize), fyne.TextStyle{}, nil)
	return measured.Width + 2*theme.InnerPadding()
}

// Styles of a field's name, in grey, its value, and the US dollar value of
// an amount, small and grey.
var statNameStyle = widget.RichTextStyle{ColorName: theme.ColorNamePlaceHolder, SizeName: statTextSize}
var statValueStyle = widget.RichTextStyle{Alignment: fyne.TextAlignTrailing, ColorName: theme.ColorNameForeground, SizeName: statTextSize}
var statUSDStyle = widget.RichTextStyle{Alignment: fyne.TextAlignTrailing, ColorName: theme.ColorNamePlaceHolder, SizeName: statUSDSize}

// statsView is the wallet statistics at the bottom of the Home tab: two
// columns of fields, each its name at the left and its value at the right
// of one line, the amounts in the left column with their worth in US
// dollars under them, the counts and the wallet's age in the right one.
type statsView struct {
	content  *fyne.Container
	received *statField
	sent     *statField
	fees     *statField
	txs      *statField
	coins    *statField
	lifetime *statField
	stats    walletStats
}

// statField is one statistic: its name, its value and, for an amount, the
// US dollar value, hidden while no rate is known. A value may come in
// several texts, the longest first, shown as the longest that fits.
type statField struct {
	name  *widget.RichText
	value *widget.RichText
	usd   *widget.RichText
	texts []string
}

func newStatField(name string, amount bool) *statField {
	var f = &statField{
		name:  widget.NewRichText(&widget.TextSegment{Text: name, Style: statNameStyle}),
		value: widget.NewRichText(&widget.TextSegment{Style: statValueStyle}),
	}
	if amount {
		f.usd = widget.NewRichText(&widget.TextSegment{Style: statUSDStyle})
		f.usd.Hide()
	}
	return f
}

// object is the field laid out by fieldLayout.
func (f *statField) object() fyne.CanvasObject {
	if f.usd == nil { return container.New(fieldLayout{f}, f.name, f.value) }
	return container.New(fieldLayout{f}, f.name, f.value, f.usd)
}

// setTexts sets the texts the value may show, the longest first.
func (f *statField) setTexts(texts ...string) {
	f.texts = texts
	setText(f.value, texts[0])
}

// setText sets the text of a rich text of one segment.
func setText(r *widget.RichText, text string) {
	var segment = r.Segments[0].(*widget.TextSegment)
	if segment.Text == text { return }
	segment.Text = text
	r.Refresh()
}

// setAmount shows the amount and its worth at the rate in cents per bitcoin,
// or no worth while the rate is unknown.
func (f *statField) setAmount(sats, rate int64) {
	f.setTexts(formatAmount(sats))
	if rate <= 0 {
		f.usd.Hide()
		return
	}
	setText(f.usd, formatUSD(usdValue(sats, rate)))
	f.usd.Show()
}

func newStatsView() *statsView {
	var v = &statsView{
		received: newStatField("Received", true),
		sent:     newStatField("Sent", true),
		fees:     newStatField("Fees", true),
		txs:      newStatField("Transactions", false),
		coins:    newStatField("Coins", false),
		lifetime: newStatField("Lifetime", false),
	}
	v.content = container.New(statsLayout{},
		v.received.object(), v.sent.object(), v.fees.object(),
		v.txs.object(), v.coins.object(), v.lifetime.object(),
	)
	return v
}

// object is the statistics in their text sizes.
func (v *statsView) object() fyne.CanvasObject {
	return container.NewThemeOverride(v.content, statsTheme{})
}

// show fills the fields in with the statistics, the amounts valued at the
// rate in cents per bitcoin, and the wallet's age as of now, in two units
// when they fit.
func (v *statsView) show(s walletStats, rate, created int64, now time.Time) {
	v.stats = s
	v.setRate(rate)
	v.txs.setTexts(fmt.Sprint(s.txs))
	v.coins.setTexts(fmt.Sprintf("%d (%d unspent)", s.coins, s.unspent))
	v.lifetime.setTexts(lifetimeText(created, now, 2), lifetimeText(created, now, 1))
	v.content.Refresh()
}

// setRate values the amounts at a new rate in cents per bitcoin.
func (v *statsView) setRate(rate int64) {
	v.received.setAmount(v.stats.received, rate)
	v.sent.setAmount(v.stats.sent, rate)
	v.fees.setAmount(v.stats.fees, rate)
	v.content.Refresh()
}

// fieldLayout lays out a field: the name at the left and the value at the
// right of its first line, one text padding apart, and the US dollar value,
// when shown, close under the value. The texts reach the field's edges,
// their outer padding hanging past them. The value shows the longest of its
// texts that fits.
type fieldLayout struct {
	f *statField
}

// line is the width of the name and a value of the width on one line.
func line(name, value float32) float32 {
	return name + value - 3*theme.InnerPadding()
}

func (l fieldLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var name, value = objects[0].MinSize(), objects[1].MinSize()
	var width = value.Width
	if len(l.f.texts) > 0 { width = statTextWidth(l.f.texts[len(l.f.texts)-1]) }
	var height = value.Height
	if len(objects) > 2 && objects[2].Visible() {
		var usd = objects[2].MinSize()
		width = max(width, usd.Width)
		height += usd.Height - usdLift()
	}
	return fyne.NewSize(line(name.Width, width), max(name.Height, height))
}

func (l fieldLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var name = objects[0].MinSize()
	for _, text := range l.f.texts {
		setText(l.f.value, text)
		if line(name.Width, objects[1].MinSize().Width) <= size.Width { break }
	}
	var value = objects[1].MinSize()
	var right = size.Width + theme.InnerPadding()
	objects[0].Resize(name)
	objects[0].Move(fyne.NewPos(-theme.InnerPadding(), 0))
	objects[1].Resize(value)
	objects[1].Move(fyne.NewPos(right-value.Width, 0))
	if len(objects) > 2 {
		var usd = objects[2].MinSize()
		objects[2].Resize(usd)
		objects[2].Move(fyne.NewPos(right-usd.Width, value.Height-usdLift()))
	}
}

// statsLayout lays out the six fields in two columns, the first three down
// the left and the others down the right, a gap apart. Each column is as
// wide as its widest field needs, and the room left is shared between
// them, so a long amount can take what short counts leave. Each row is as
// tall as the taller of its two fields.
type statsLayout struct{}

// statsColumnGap is the least space between the two columns.
func statsColumnGap() float32 {
	return 2 * coinsGap()
}

// rowHeight is the height of the row of fields i and i+3.
func rowHeight(objects []fyne.CanvasObject, i int) float32 {
	return max(objects[i].MinSize().Height, objects[i+3].MinSize().Height)
}

// columnWidths is the width each column needs.
func columnWidths(objects []fyne.CanvasObject) (float32, float32) {
	var left, right = float32(0), float32(0)
	for i := 0; i < 3; i++ {
		left = max(left, objects[i].MinSize().Width)
		right = max(right, objects[i+3].MinSize().Width)
	}
	return left, right
}

func (statsLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var left, right = columnWidths(objects)
	var height = float32(0)
	for i := 0; i < 3; i++ {
		height += rowHeight(objects, i)
	}
	return fyne.NewSize(left+right+statsColumnGap(), height)
}

func (statsLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var left, right = columnWidths(objects)
	var spare = (size.Width - statsColumnGap() - left - right) / 2
	left += spare
	right += spare
	var y = float32(0)
	for i := 0; i < 3; i++ {
		var height = rowHeight(objects, i)
		objects[i].Resize(fyne.NewSize(left, height))
		objects[i].Move(fyne.NewPos(0, y))
		objects[i+3].Resize(fyne.NewSize(right, height))
		objects[i+3].Move(fyne.NewPos(size.Width-right, y))
		y += height
	}
}
