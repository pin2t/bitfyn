package gui

import "fmt"
import "image/color"
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
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/sync"
import "bitfyn/internal/wallet"

// mergeIcon and splitIcon are the Material Design "call_merge" and
// "call_split" icons, recoloured with the theme.
var mergeIcon = theme.NewThemedResource(fyne.NewStaticResource("merge.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">`+
		`<path d="M17 20.41L18.41 19 15 15.59 13.59 17 17 20.41zM7.5 8H11v5.59L5.59 19 7 20.41l6-6V8h3.5L12 3.5 7.5 8z"/></svg>`)))
var splitIcon = theme.NewThemedResource(fyne.NewStaticResource("split.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">`+
		`<path d="M14 4l2.29 2.29-2.88 2.88 1.42 1.42 2.88-2.88L20 10V4zm-4 0H4v6l2.29-2.29 4.71 4.7V20h2v-8.41l-5.29-5.3z"/></svg>`)))

// arrowUp and arrowDown are the small triangles of the sort controls.
var arrowUp = fyne.NewStaticResource("sort-up.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" width="12" height="8" viewBox="0 0 12 8"><path d="M1 7.5L6 .5l5 7z"/></svg>`))
var arrowDown = fyne.NewStaticResource("sort-down.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" width="12" height="8" viewBox="0 0 12 8"><path d="M1 .5h10L6 7.5z"/></svg>`))

// arrowWidth and arrowHeight size one sort triangle; copyIconSize sizes the
// copy icon on the cards.
const arrowWidth = 10
const arrowHeight = 7
const copyIconSize = 16

// coinSort is a column the coin list is sorted by.
type coinSort int

const sortByTime coinSort = 0
const sortByAmount coinSort = 1

// coinsView is the Coins tab: a header naming the columns, with sort arrows
// on Time and Amount, over a list of cards, one per spendable coin, followed
// by the Merge, Split and Send buttons. A list taller than the window
// scrolls, keeping the buttons in view. Each card has a check box to select
// its coin, and the header one to select them all.
type coinsView struct {
	g          *gui
	content    *fyne.Container
	list       *fyne.Container
	empty      *widget.Label
	selectAll  *widget.Check
	checks     []*widget.Check
	selected   map[wire.OutPoint]bool
	timeSort   *sortArrows
	amountSort *sortArrows
	merge      *widget.Button
	split      *widget.Button
	send       *widget.Button
	coins      []wallet.Coin
	shown      string
	times      []*widget.Label
	by         coinSort
	ascending  bool
	now        time.Time
}

// newCoinsView builds the Coins tab, empty, sorted newest first. Merge and
// Split are disabled for now: they will merge the selected coins to one
// address and split one coin to many.
func newCoinsView(g *gui) *coinsView {
	var v = &coinsView{g: g, selected: map[wire.OutPoint]bool{}, by: sortByTime, now: time.Now()}
	v.list = container.NewVBox()
	v.empty = widget.NewLabelWithStyle("No coins yet", fyne.TextAlignCenter, fyne.TextStyle{})
	v.selectAll = widget.NewCheck("", v.setAll)
	v.timeSort = newSortArrows(func(ascending bool) { v.sort(sortByTime, ascending) })
	v.amountSort = newSortArrows(func(ascending bool) { v.sort(sortByAmount, ascending) })
	var header = container.New(coinColumns{},
		v.selectAll,
		container.NewHBox(boldLabel("Time"), container.NewCenter(v.timeSort)),
		boldLabel("Address"),
		container.NewHBox(layout.NewSpacer(), boldLabel("Amount"), container.NewCenter(v.amountSort)),
	)
	v.merge = widget.NewButtonWithIcon("Merge", mergeIcon, nil)
	v.merge.Disable()
	v.split = widget.NewButtonWithIcon("Split", splitIcon, nil)
	v.split.Disable()
	v.send = widget.NewButtonWithIcon("Send", theme.UploadIcon(), g.showSend)
	var sides = layout.NewCustomPaddedLayout(0, 0, coinsGap(), coinsGap())
	v.content = container.New(coinsLayout{v},
		container.New(sides, container.NewPadded(header)),
		container.NewStack(container.NewCenter(v.empty), container.NewVScroll(container.New(sides, v.list))),
		container.New(sides, container.NewGridWithColumns(3, v.merge, v.split, v.send)),
	)
	v.showArrows()
	v.showSelectAll()
	return v
}

// coinsGap is the space at either side of the cards and the buttons, and
// under the buttons.
func coinsGap() float32 {
	return 2 * theme.InnerPadding()
}

// coinsLayout puts the header at the top, the list under it as tall as its
// cards, and the buttons right after the list, a gap above the window's
// bottom. When the cards do not fit, the list takes the height left and
// scrolls, the buttons staying in view.
type coinsLayout struct {
	v *coinsView
}

// listHeight is the height the list needs to show every card, or the empty
// placeholder.
func (l coinsLayout) listHeight() float32 {
	if len(l.v.list.Objects) == 0 {
		return l.v.empty.MinSize().Height
	}
	return l.v.list.MinSize().Height
}

func (l coinsLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var header, buttons = objects[0].MinSize(), objects[2].MinSize()
	var list = l.v.empty.MinSize().Height
	return fyne.NewSize(max(header.Width, buttons.Width),
		header.Height+list+theme.Padding()+buttons.Height+coinsGap())
}

func (l coinsLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var header, list, buttons = objects[0], objects[1], objects[2]
	var headerHeight, buttonsHeight = header.MinSize().Height, buttons.MinSize().Height
	var room = size.Height - headerHeight - theme.Padding() - buttonsHeight - coinsGap()
	var listHeight = max(min(l.listHeight(), room), 0)
	header.Resize(fyne.NewSize(size.Width, headerHeight))
	header.Move(fyne.NewPos(0, 0))
	list.Resize(fyne.NewSize(size.Width, listHeight))
	list.Move(fyne.NewPos(0, headerHeight))
	buttons.Resize(fyne.NewSize(size.Width, buttonsHeight))
	buttons.Move(fyne.NewPos(0, headerHeight+listHeight+theme.Padding()))
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

// rebuild sorts the coins and makes a card for each. Coins no longer listed
// drop out of the selection.
func (v *coinsView) rebuild() {
	sortCoins(v.coins, v.by, v.ascending)
	var listed = make(map[wire.OutPoint]bool, len(v.coins))
	var cards = make([]fyne.CanvasObject, len(v.coins))
	v.times = make([]*widget.Label, len(v.coins))
	v.checks = make([]*widget.Check, len(v.coins))
	for i, c := range v.coins {
		listed[c.OutPoint] = true
		cards[i], v.times[i], v.checks[i] = v.card(c)
	}
	for op := range v.selected {
		if !listed[op] { delete(v.selected, op) }
	}
	v.showSelectAll()
	v.list.Objects = cards
	v.list.Refresh()
	if len(v.coins) == 0 {
		v.empty.Show()
	} else {
		v.empty.Hide()
	}
	v.content.Refresh()
	v.showArrows()
}

// setAll selects every coin or none, from the header check box.
func (v *coinsView) setAll(on bool) {
	for i, c := range v.coins {
		v.choose(c.OutPoint, on)
		v.checks[i].Checked = on
		v.checks[i].Refresh()
	}
	v.showSelectAll()
}

// choose adds the coin to the selection or takes it out.
func (v *coinsView) choose(op wire.OutPoint, on bool) {
	if on {
		v.selected[op] = true
	} else {
		delete(v.selected, op)
	}
}

// showSelectAll sets the header check box from the selection: checked when
// every coin is selected, partly when some are, and disabled without coins.
// The fields are set directly so its OnChanged does not fire.
func (v *coinsView) showSelectAll() {
	var n = len(v.selected)
	v.selectAll.Checked = n > 0 && n == len(v.coins)
	v.selectAll.Partial = n > 0 && n < len(v.coins)
	if len(v.coins) == 0 {
		v.selectAll.Disable()
	} else {
		v.selectAll.Enable()
	}
	v.selectAll.Refresh()
}

// selection returns the selected coins in list order.
func (v *coinsView) selection() []wallet.Coin {
	var out []wallet.Coin
	for _, c := range v.coins {
		if v.selected[c.OutPoint] { out = append(out, c) }
	}
	return out
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

// card is one coin: a check box selecting it, when it appeared, its address
// shortened to the room left by the amount, with a copy icon for the whole
// address, and its value. An unconfirmed coin's text is grey. It returns the
// time label too, to keep the time current, and the check box.
func (v *coinsView) card(c wallet.Coin) (fyne.CanvasObject, *widget.Label, *widget.Check) {
	var check = widget.NewCheck("", func(on bool) {
		v.choose(c.OutPoint, on)
		v.showSelectAll()
	})
	check.Checked = v.selected[c.OutPoint]
	var when = widget.NewLabel(relativeTime(c.Time, v.now))
	var address = widget.NewLabel(c.Address)
	var copyIcon = newTapIcon(theme.ContentCopyIcon(), copyIconSize, func() { v.g.copyAddress(c.Address) })
	var cells = []fyne.CanvasObject{
		when,
		container.New(addressFit{address: c.Address, label: address}, address, container.NewCenter(copyIcon)),
		container.NewHBox(layout.NewSpacer(), widget.NewLabel(formatAmount(c.Value))),
	}
	if !c.Confirmed {
		for i, cell := range cells {
			cells[i] = container.NewThemeOverride(cell, pendingTheme{})
		}
	}
	var row = container.New(coinColumns{}, append([]fyne.CanvasObject{check}, cells...)...)
	var background = canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground))
	background.CornerRadius = theme.InputRadiusSize()
	return container.NewStack(background, container.NewPadded(row)), when, check
}

// pendingTheme draws the text and icons of an unconfirmed coin in the grey
// of the app theme's placeholder text, leaving everything else to that theme.
type pendingTheme struct{}

func (pendingTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	var base = fyne.CurrentApp().Settings().Theme()
	if name == theme.ColorNameForeground { name = theme.ColorNamePlaceHolder }
	return base.Color(name, variant)
}

func (pendingTheme) Font(style fyne.TextStyle) fyne.Resource {
	return fyne.CurrentApp().Settings().Theme().Font(style)
}

func (pendingTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return fyne.CurrentApp().Settings().Theme().Icon(name)
}

func (pendingTheme) Size(name fyne.ThemeSizeName) float32 {
	return fyne.CurrentApp().Settings().Theme().Size(name)
}

// addressTail is how many characters a shortened address keeps of its end.
const addressTail = 6

// fitAddress returns the address whole if it fits, else its start and its
// last addressTail characters around an ellipsis, the start as long as fits
// but at least five characters, "bc1q" and one more, or else dropped:
// "…ryt4xl".
func fitAddress(address string, fits func(string) bool) string {
	if len(address) <= addressTail+1 || fits(address) { return address }
	var end = address[len(address)-addressTail:]
	var lo, hi = 5, len(address) - addressTail - 1
	var best = -1
	for lo <= hi {
		var mid = (lo + hi) / 2
		if fits(address[:mid] + "…" + end) {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best < 0 { return "…" + end }
	return address[:best] + "…" + end
}

// labelWidth is the width of a label showing the text.
func labelWidth(text string) float32 {
	return textWidth(text, theme.SizeNameText, fyne.TextStyle{}) + 2*theme.InnerPadding()
}

// addressFit lays out an address cell: the label, showing as much of the
// address as fits beside the copy icon, then the icon right after the text.
type addressFit struct {
	address string
	label   *widget.Label
}

func (f addressFit) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var icon = objects[1].MinSize()
	var shortest = fitAddress(f.address, func(string) bool { return false })
	return fyne.NewSize(labelWidth(shortest)+theme.Padding()+icon.Width, max(f.label.MinSize().Height, icon.Height))
}

func (f addressFit) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var icon = objects[1]
	var iconSize = icon.MinSize()
	var room = size.Width - theme.Padding() - iconSize.Width
	var text = fitAddress(f.address, func(s string) bool { return labelWidth(s) <= room })
	if f.label.Text != text { f.label.SetText(text) }
	var labelSize = fyne.NewSize(labelWidth(text), f.label.MinSize().Height)
	f.label.Resize(labelSize)
	f.label.Move(fyne.NewPos(0, (size.Height-labelSize.Height)/2))
	icon.Resize(iconSize)
	icon.Move(fyne.NewPos(labelSize.Width+theme.Padding(), (size.Height-iconSize.Height)/2))
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
// up: the check box column, the time column, wide enough for the longest
// relative time, the amount at the right, as wide as it needs, and the
// address column taking the rest. Each cell is centred vertically.
type coinColumns struct{}

// timeColumnWidth is the width of the time column: the longest relative
// time it usually holds, with label padding and the sort arrows.
func timeColumnWidth() float32 {
	return textWidth("11 months ago", theme.SizeNameText, fyne.TextStyle{}) + 2*theme.InnerPadding() + arrowWidth
}


func (coinColumns) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var width = objects[0].MinSize().Width + max(timeColumnWidth(), objects[1].MinSize().Width) + objects[2].MinSize().Width + objects[3].MinSize().Width
	var height = float32(0)
	for _, o := range objects {
		height = max(height, o.MinSize().Height)
	}
	return fyne.NewSize(width, height)
}

func (coinColumns) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var checkWidth = objects[0].MinSize().Width
	var timeWidth = max(timeColumnWidth(), objects[1].MinSize().Width)
	var amountWidth = objects[3].MinSize().Width
	var cells = []struct {
		x, width float32
	}{
		{0, checkWidth},
		{checkWidth, timeWidth},
		{checkWidth + timeWidth, max(size.Width-checkWidth-timeWidth-amountWidth, 0)},
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
