package gui

import "fmt"
import "sort"
import "strings"
import "time"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/canvas"
import "fyne.io/fyne/v2/container"
import "fyne.io/fyne/v2/dialog"
import "fyne.io/fyne/v2/driver/desktop"
import "fyne.io/fyne/v2/layout"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "bitfyn/internal/sync"
import "bitfyn/internal/wallet"

// hourglassIcon marks a coin that is not confirmed yet, the Material Design
// "hourglass_empty" icon like the theme's own icons, recoloured with the
// theme.
var hourglassIcon = theme.NewThemedResource(fyne.NewStaticResource("hourglass.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">`+
		`<path d="M6 2v6h.01L6 8.01 10 12l-4 4 .01.01H6V22h12v-5.99h-.01L18 16l-4-4 4-3.99-.01-.01H18V2H6z`+
		`m10 14.5V20H8v-3.5l4-4 4 4zm-4-5l-4-4V4h8v3.5l-4 4z"/></svg>`)))

// arrowUp and arrowDown are the small triangles of the sort controls.
var arrowUp = fyne.NewStaticResource("sort-up.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" width="12" height="8" viewBox="0 0 12 8"><path d="M1 7.5L6 .5l5 7z"/></svg>`))
var arrowDown = fyne.NewStaticResource("sort-down.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" width="12" height="8" viewBox="0 0 12 8"><path d="M1 .5h10L6 7.5z"/></svg>`))

// arrowWidth and arrowHeight size one sort triangle; copyIconSize sizes the
// copy icon on the cards and hourglassSize the unconfirmed mark.
const arrowWidth = 10
const arrowHeight = 7
const copyIconSize = 16
const hourglassSize = 16

// coinSort is a column the coin list is sorted by.
type coinSort int

const sortByTime coinSort = 0
const sortByAmount coinSort = 1

// coinsView is the Coins tab: a header naming the columns, with sort arrows
// on Time and Amount, over a scrolling list of cards, one per spendable coin,
// and the Send button at the bottom.
type coinsView struct {
	g          *gui
	content    fyne.CanvasObject
	list       *fyne.Container
	empty      *widget.Label
	timeSort   *sortArrows
	amountSort *sortArrows
	send       *widget.Button
	coins      []wallet.Coin
	shown      string
	times      []*widget.Label
	by         coinSort
	ascending  bool
	now        time.Time
}

// newCoinsView builds the Coins tab, empty, sorted newest first.
func newCoinsView(g *gui) *coinsView {
	var v = &coinsView{g: g, by: sortByTime, now: time.Now()}
	v.list = container.NewVBox()
	v.empty = widget.NewLabelWithStyle("No coins yet", fyne.TextAlignCenter, fyne.TextStyle{})
	v.timeSort = newSortArrows(func(ascending bool) { v.sort(sortByTime, ascending) })
	v.amountSort = newSortArrows(func(ascending bool) { v.sort(sortByAmount, ascending) })
	var header = container.New(coinColumns{},
		container.NewHBox(boldLabel("Time"), container.NewCenter(v.timeSort)),
		boldLabel("Address"),
		container.NewHBox(layout.NewSpacer(), boldLabel("Amount"), container.NewCenter(v.amountSort)),
	)
	v.send = widget.NewButtonWithIcon("Send", theme.UploadIcon(), g.showSend)
	v.content = container.NewBorder(
		container.NewPadded(header),
		container.NewCenter(atLeastWide(v.send, actionWidth)),
		nil, nil,
		container.NewStack(container.NewCenter(v.empty), container.NewVScroll(v.list)),
	)
	v.showArrows()
	return v
}

func boldLabel(text string) *widget.Label {
	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

// update shows the coins as of now. The cards are rebuilt only when the coins
// changed; otherwise only their times are brought up to date.
func (v *coinsView) update(coins []wallet.Coin, now time.Time) {
	v.now = now
	var key = coinsKey(coins)
	if key == v.shown {
		v.refreshTimes()
		return
	}
	v.shown = key
	v.coins = append([]wallet.Coin(nil), coins...)
	v.rebuild()
}

// coinsKey identifies a coin list by its coins and their state.
func coinsKey(coins []wallet.Coin) string {
	var b strings.Builder
	for _, c := range coins {
		fmt.Fprintf(&b, "%s/%d/%t/%d;", c.OutPoint, c.Value, c.Confirmed, c.Time)
	}
	return b.String()
}

// sort orders the cards by the column, then shows the chosen arrow.
func (v *coinsView) sort(by coinSort, ascending bool) {
	v.by = by
	v.ascending = ascending
	v.rebuild()
}

// rebuild sorts the coins and makes a card for each.
func (v *coinsView) rebuild() {
	sortCoins(v.coins, v.by, v.ascending)
	var cards = make([]fyne.CanvasObject, len(v.coins))
	v.times = make([]*widget.Label, len(v.coins))
	for i, c := range v.coins {
		cards[i], v.times[i] = v.card(c)
	}
	v.list.Objects = cards
	v.list.Refresh()
	if len(v.coins) == 0 {
		v.empty.Show()
	} else {
		v.empty.Hide()
	}
	v.showArrows()
}

// sortCoins orders coins by time or amount, ties broken by the outpoint so
// the order is stable across updates.
func sortCoins(coins []wallet.Coin, by coinSort, ascending bool) {
	sort.SliceStable(coins, func(i, j int) bool {
		var a, b = coins[i], coins[j]
		if !ascending {
			a, b = b, a
		}
		var ka, kb = a.Time, b.Time
		if by == sortByAmount {
			ka, kb = a.Value, b.Value
		}
		if ka != kb { return ka < kb }
		return a.OutPoint.String() < b.OutPoint.String()
	})
}

// showArrows highlights the arrow of the current sort order.
func (v *coinsView) showArrows() {
	v.timeSort.show(v.by == sortByTime, v.ascending)
	v.amountSort.show(v.by == sortByAmount, v.ascending)
}

// refreshTimes rewrites the relative times of the cards as of now.
func (v *coinsView) refreshTimes() {
	for i, label := range v.times {
		label.SetText(relativeTime(v.coins[i].Time, v.now))
	}
}

// card is one coin: when it appeared, its short address with a copy icon, and
// its value, marked with an hourglass while unconfirmed. It returns the time
// label too, to keep the time current.
func (v *coinsView) card(c wallet.Coin) (fyne.CanvasObject, *widget.Label) {
	var when = widget.NewLabel(relativeTime(c.Time, v.now))
	var address = widget.NewLabel(shortAddress(c.Address))
	var copyIcon = newTapIcon(theme.ContentCopyIcon(), copyIconSize, func() { v.g.copyAddress(c.Address) })
	var amount = []fyne.CanvasObject{layout.NewSpacer()}
	if !c.Confirmed {
		amount = append(amount, container.NewCenter(newTapIcon(hourglassIcon, hourglassSize, nil)))
	}
	amount = append(amount, widget.NewLabel(formatAmount(c.Value)))
	var row = container.New(coinColumns{},
		when,
		container.NewHBox(address, container.NewCenter(copyIcon)),
		container.NewHBox(amount...),
	)
	var background = canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground))
	background.CornerRadius = theme.InputRadiusSize()
	return container.NewStack(background, container.NewPadded(row)), when
}

// tickTimes brings the relative coin times up to date every minute until
// done is closed.
func (g *gui) tickTimes(done <-chan struct{}) {
	var ticker = time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case now := <-ticker.C:
			fyne.Do(func() { g.coinsView.update(sync.Coins(), now) })
		}
	}
}

// copyAddress puts the address on the clipboard and says so.
func (g *gui) copyAddress(address string) {
	fyne.CurrentApp().Clipboard().SetContent(address)
	dialog.ShowInformation("Copied", "Address copied to clipboard", g.window)
}

// relativeTime says how long ago the unix time was, in its largest whole
// unit: "just now", "5 minutes ago", "1 day ago", up to years. Unknown and
// future times, as block times may be slightly ahead, read "just now" and
// "—".
func relativeTime(unix int64, now time.Time) string {
	if unix == 0 {
		return "—"
	}
	var age = now.Sub(time.Unix(unix, 0))
	var units = []struct {
		size time.Duration
		name string
	}{
		{365 * 24 * time.Hour, "year"},
		{30 * 24 * time.Hour, "month"},
		{24 * time.Hour, "day"},
		{time.Hour, "hour"},
		{time.Minute, "minute"},
	}
	for _, u := range units {
		if n := int(age / u.size); n >= 1 {
			if n == 1 {
				return "1 " + u.name + " ago"
			}
			return fmt.Sprintf("%d %ss ago", n, u.name)
		}
	}
	return "just now"
}

// coinColumns lays out the header and every card alike, so their columns line
// up: the time column, wide enough for the longest relative time, the amount
// column, wide enough for a large amount, at the right, and the address
// column taking the rest. Each cell is centred vertically.
type coinColumns struct{}

// timeColumnWidth and amountColumnWidth are the widths of the time and
// amount columns: the longest texts they usually hold, with label padding,
// the unconfirmed mark and the sort arrows.
func timeColumnWidth() float32 {
	return textWidth("11 months ago", theme.SizeNameText, fyne.TextStyle{}) + 2*theme.InnerPadding() + arrowWidth
}

func amountColumnWidth() float32 {
	return textWidth("4 999 999 sats", theme.SizeNameText, fyne.TextStyle{}) + 2*theme.InnerPadding() + hourglassSize + theme.Padding()
}

func (coinColumns) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var width = max(timeColumnWidth(), objects[0].MinSize().Width) + objects[1].MinSize().Width + max(amountColumnWidth(), objects[2].MinSize().Width)
	var height = float32(0)
	for _, o := range objects {
		height = max(height, o.MinSize().Height)
	}
	return fyne.NewSize(width, height)
}

func (coinColumns) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var timeWidth = max(timeColumnWidth(), objects[0].MinSize().Width)
	var amountWidth = max(amountColumnWidth(), objects[2].MinSize().Width)
	var cells = []struct {
		x, width float32
	}{
		{0, timeWidth},
		{timeWidth, max(size.Width-timeWidth-amountWidth, 0)},
		{size.Width - amountWidth, amountWidth},
	}
	for i, o := range objects {
		var height = o.MinSize().Height
		o.Resize(fyne.NewSize(cells[i].width, height))
		o.Move(fyne.NewPos(cells[i].x, (size.Height-height)/2))
	}
}

// sortArrows is a pair of small triangles, up for ascending over down for
// descending: tapping one sorts by its column in that order. The arrow of the
// order in use is drawn in the primary colour, the others in grey.
type sortArrows struct {
	widget.BaseWidget
	up   *tapIcon
	down *tapIcon
}

func newSortArrows(onSort func(ascending bool)) *sortArrows {
	var s = &sortArrows{}
	s.up = newTapIcon(arrowUp, 0, func() { onSort(true) })
	s.down = newTapIcon(arrowDown, 0, func() { onSort(false) })
	s.up.size = fyne.NewSize(arrowWidth, arrowHeight)
	s.down.size = fyne.NewSize(arrowWidth, arrowHeight)
	s.ExtendBaseWidget(s)
	return s
}

// show colours the arrows: active is whether the list is sorted by this
// column, ascending in which order.
func (s *sortArrows) show(active, ascending bool) {
	var colour = func(on bool) fyne.ThemeColorName {
		if on { return theme.ColorNamePrimary }
		return theme.ColorNamePlaceHolder
	}
	s.up.setResource(theme.NewColoredResource(arrowUp, colour(active && ascending)))
	s.down.setResource(theme.NewColoredResource(arrowDown, colour(active && !ascending)))
}

func (s *sortArrows) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.New(arrowsLayout{}, s.up, s.down))
}

// arrowsLayout stacks the two arrows with a small gap.
type arrowsLayout struct{}

func (arrowsLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(arrowWidth, 2*arrowHeight+theme.Padding()/2)
}

func (arrowsLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var x = (size.Width - arrowWidth) / 2
	var y = (size.Height - 2*arrowHeight - theme.Padding()/2) / 2
	objects[0].Resize(fyne.NewSize(arrowWidth, arrowHeight))
	objects[0].Move(fyne.NewPos(x, y))
	objects[1].Resize(fyne.NewSize(arrowWidth, arrowHeight))
	objects[1].Move(fyne.NewPos(x, y+arrowHeight+theme.Padding()/2))
}

// tapIcon is an icon of a set size that calls onTap when tapped, showing the
// pointer cursor, or a plain icon when onTap is nil.
type tapIcon struct {
	widget.BaseWidget
	icon  *widget.Icon
	size  fyne.Size
	onTap func()
}

func newTapIcon(res fyne.Resource, size float32, onTap func()) *tapIcon {
	var t = &tapIcon{icon: widget.NewIcon(res), size: fyne.NewSquareSize(size), onTap: onTap}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tapIcon) setResource(res fyne.Resource) {
	t.icon.SetResource(res)
}

func (t *tapIcon) Tapped(*fyne.PointEvent) {
	if t.onTap != nil { t.onTap() }
}

func (t *tapIcon) Cursor() desktop.Cursor {
	if t.onTap == nil { return desktop.DefaultCursor }
	return desktop.PointerCursor
}

func (t *tapIcon) MinSize() fyne.Size { return t.size }

func (t *tapIcon) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(t.icon)
}
