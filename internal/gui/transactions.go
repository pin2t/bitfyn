package gui

import "fmt"
import "sort"
import "strings"
import "time"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/canvas"
import "fyne.io/fyne/v2/container"
import "fyne.io/fyne/v2/layout"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "bitfyn/internal/sync"

// forwardIcon is the Material Design "arrow_forward" icon, recoloured with
// the theme, pointing from a transaction's inputs to its outputs.
var forwardIcon = theme.NewThemedResource(fyne.NewStaticResource("forward.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">`+
		`<path d="M12 4l-1.41 1.41L16.17 11H4v2h12.17l-5.58 5.59L12 20l8-8z"/></svg>`)))

// txPage is how many cards are added at a time as the list scrolls down.
const txPage = 30

// txAddressRows is the most addresses a card lists on either side; past it
// the last row counts the ones left out.
const txAddressRows = 6

// unknownAddress stands for an input address that cannot be told.
const unknownAddress = "unknown"

// txView is the Transactions tab: a header with sort arrows on Time and
// Amount over a list of cards, one per wallet transaction. The list scrolls,
// and cards are added a page at a time as it nears its end, so a long
// history costs no more than the part looked at.
type txView struct {
	g          *gui
	content    *fyne.Container
	list       *fyne.Container
	scroll     *container.Scroll
	empty      *widget.Label
	timeSort   *sortArrows
	amountSort *sortArrows
	txs        []sync.WalletTx
	cards      []txCard
	shown      string
	by         coinSort
	ascending  bool
	now        time.Time
}

// txCard holds the labels of one card that tests and time updates reach.
type txCard struct {
	when    *widget.Label
	id      *widget.Label
	amount  *widget.Label
	inputs  []*widget.Label
	outputs []*widget.Label
}

// newTxView builds the Transactions tab, empty, sorted newest first. The
// cards keep the Coins tab's gaps at either side.
func newTxView(g *gui) *txView {
	var v = &txView{g: g, by: sortByTime, now: time.Now()}
	v.list = container.NewVBox()
	v.empty = widget.NewLabelWithStyle("No transactions yet", fyne.TextAlignCenter, fyne.TextStyle{})
	v.timeSort = newSortArrows(func(ascending bool) { v.sort(sortByTime, ascending) })
	v.amountSort = newSortArrows(func(ascending bool) { v.sort(sortByAmount, ascending) })
	var header = container.New(txColumns{},
		container.NewHBox(boldLabel("Time"), container.NewCenter(v.timeSort)),
		layout.NewSpacer(),
		container.NewHBox(layout.NewSpacer(), boldLabel("Amount"), container.NewCenter(v.amountSort)),
	)
	var sides = layout.NewCustomPaddedLayout(0, 0, coinsGap(), coinsGap())
	v.scroll = container.NewVScroll(container.New(sides, v.list))
	v.scroll.OnScrolled = func(fyne.Position) { v.more() }
	v.content = container.NewBorder(
		container.New(sides, container.NewPadded(header)), nil, nil, nil,
		container.New(layout.NewCustomPaddedLayout(0, coinsGap(), 0, 0),
			container.NewStack(container.NewCenter(v.empty), v.scroll)),
	)
	v.showArrows()
	return v
}

// update shows the transactions as of now. The cards are rebuilt only when
// the transactions changed, as many as were shown; otherwise only their
// times are brought up to date.
func (v *txView) update(txs []sync.WalletTx, now time.Time) {
	v.now = now
	var key = txsKey(txs)
	if key == v.shown {
		v.refreshTimes()
		return
	}
	v.shown = key
	v.txs = append([]sync.WalletTx(nil), txs...)
	v.rebuild(max(len(v.cards), txPage))
}

// txsKey identifies a transaction list by its transactions and their state.
func txsKey(txs []sync.WalletTx) string {
	var b strings.Builder
	for _, t := range txs {
		fmt.Fprintf(&b, "%s/%t/%d;", t.Txid, t.Confirmed, t.Time)
	}
	return b.String()
}

// sort orders the cards by the column from the top of the list.
func (v *txView) sort(by coinSort, ascending bool) {
	v.by = by
	v.ascending = ascending
	v.scroll.ScrollToTop()
	v.rebuild(txPage)
}

// rebuild sorts the transactions and makes cards for the first n.
func (v *txView) rebuild(n int) {
	sortTxs(v.txs, v.by, v.ascending)
	v.cards = nil
	v.list.Objects = nil
	v.add(n)
	if len(v.txs) == 0 {
		v.empty.Show()
	} else {
		v.empty.Hide()
	}
	v.showArrows()
	v.more()
}

// add makes cards for up to n more transactions at the end of the list.
func (v *txView) add(n int) {
	for _, t := range v.txs[len(v.cards):min(len(v.cards)+n, len(v.txs))] {
		var card, labels = v.card(t)
		v.cards = append(v.cards, labels)
		v.list.Objects = append(v.list.Objects, card)
	}
	v.list.Refresh()
	v.scroll.Refresh()
}

// more adds a page of cards while the end of the list is less than a
// screenful below the view.
func (v *txView) more() {
	var view = v.scroll.Size().Height
	for len(v.cards) < len(v.txs) {
		var below = v.scroll.Content.MinSize().Height - v.scroll.Offset.Y - view
		if below >= view { return }
		v.add(txPage)
	}
}

// sortTxs orders transactions by time or net amount, ties broken by the
// transaction id so the order is stable across updates.
func sortTxs(txs []sync.WalletTx, by coinSort, ascending bool) {
	sort.SliceStable(txs, func(i, j int) bool {
		var a, b = txs[i], txs[j]
		if !ascending {
			a, b = b, a
		}
		var ka, kb = a.Time, b.Time
		if by == sortByAmount {
			ka, kb = a.Net, b.Net
		}
		if ka != kb { return ka < kb }
		return a.Txid.String() < b.Txid.String()
	})
}

// showArrows highlights the arrow of the current sort order.
func (v *txView) showArrows() {
	v.timeSort.show(v.by == sortByTime, v.ascending)
	v.amountSort.show(v.by == sortByAmount, v.ascending)
}

// refreshTimes rewrites the relative times of the cards as of now.
func (v *txView) refreshTimes() {
	for i, c := range v.cards {
		c.when.SetText(relativeTime(v.txs[i].Time, v.now))
	}
}

// txAmount is what a transaction did to the wallet balance, signed, as in
// "+ 1 000 sats" or "- 0.5 BTC".
func txAmount(net int64) string {
	switch {
	case net > 0:
		return "+ " + formatAmount(net)
	case net < 0:
		return "- " + formatAmount(-net)
	}
	return formatAmount(0)
}

// card is one transaction: on top when it was confirmed or seen, its id
// shortened to fit with a copy icon for the whole id, and its net amount;
// under them the addresses it spends from at the left, an arrow, and the
// addresses it pays to at the right, the wallet's own in bold. An
// unconfirmed transaction's text is grey.
func (v *txView) card(t sync.WalletTx) (fyne.CanvasObject, txCard) {
	var id = t.Txid.String()
	var c = txCard{
		when:   widget.NewLabel(relativeTime(t.Time, v.now)),
		id:     widget.NewLabel(id),
		amount: widget.NewLabel(txAmount(t.Net)),
	}
	var copyIcon = newTapIcon(theme.ContentCopyIcon(), copyIconSize, func() { v.g.copyText(id, "Transaction id") })
	var top = container.New(txColumns{},
		c.when,
		container.New(addressFit{address: id, label: c.id}, c.id, container.NewCenter(copyIcon)),
		container.NewHBox(layout.NewSpacer(), c.amount),
	)
	var inputs, outputs fyne.CanvasObject
	inputs, c.inputs = addressList(t.Inputs, false)
	outputs, c.outputs = addressList(t.Outputs, true)
	var arrow = widget.NewIcon(forwardIcon)
	var body = fyne.CanvasObject(container.NewVBox(top, container.New(flowLayout{}, inputs, arrow, outputs)))
	if !t.Confirmed {
		body = container.NewThemeOverride(body, pendingTheme{})
	}
	var background = canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground))
	background.CornerRadius = theme.InputRadiusSize()
	return container.NewStack(background, container.NewPadded(body)), c
}

// addressList stacks the addresses closely, each shortened to the room it
// gets, the wallet's own in bold and unknown ones in italics, aligned to the
// right when trailing. Past txAddressRows the last row says how many more
// there are.
func addressList(addrs []sync.TxAddress, trailing bool) (fyne.CanvasObject, []*widget.Label) {
	var rows []fyne.CanvasObject
	var labels []*widget.Label
	for i, a := range addrs {
		if i == txAddressRows-1 && len(addrs) > txAddressRows {
			var rest = widget.NewLabel(fmt.Sprintf("%d more", len(addrs)-i))
			rest.TextStyle.Italic = true
			if trailing { rest.Alignment = fyne.TextAlignTrailing }
			rows = append(rows, rest)
			labels = append(labels, rest)
			break
		}
		var text = a.Address
		if text == "" { text = unknownAddress }
		var label = widget.NewLabel(text)
		label.TextStyle = fyne.TextStyle{Bold: a.Mine, Italic: a.Address == ""}
		rows = append(rows, container.New(textFit{text: text, label: label, trailing: trailing}, label))
		labels = append(labels, label)
	}
	return container.New(layout.NewCustomPaddedVBoxLayout(0), rows...), labels
}

// textFit lays out a label showing as much of the address as fits, at the
// left of its cell or, when trailing, at the right.
type textFit struct {
	text     string
	label    *widget.Label
	trailing bool
}

func (f textFit) MinSize([]fyne.CanvasObject) fyne.Size {
	var shortest = fitAddress(f.text, func(string) bool { return false })
	return fyne.NewSize(styledLabelWidth(shortest, f.label.TextStyle), f.label.MinSize().Height)
}

func (f textFit) Layout(_ []fyne.CanvasObject, size fyne.Size) {
	var text = fitAddress(f.text, func(s string) bool { return styledLabelWidth(s, f.label.TextStyle) <= size.Width })
	if f.label.Text != text { f.label.SetText(text) }
	var width = styledLabelWidth(text, f.label.TextStyle)
	var x = float32(0)
	if f.trailing { x = size.Width - width }
	f.label.Resize(fyne.NewSize(width, size.Height))
	f.label.Move(fyne.NewPos(x, 0))
}

// flowLayout lays out a card's addresses: the inputs in the left half, the
// outputs in the right half and the arrow between them, centred
// vertically.
type flowLayout struct{}

func (flowLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var inputs, arrow, outputs = objects[0].MinSize(), objects[1].MinSize(), objects[2].MinSize()
	var side = max(inputs.Width, outputs.Width)
	return fyne.NewSize(2*side+arrow.Width+2*theme.Padding(), max(inputs.Height, arrow.Height, outputs.Height))
}

func (flowLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var inputs, arrow, outputs = objects[0], objects[1], objects[2]
	var arrowSize = arrow.MinSize()
	var side = max((size.Width-arrowSize.Width)/2-theme.Padding(), 0)
	inputs.Resize(fyne.NewSize(side, inputs.MinSize().Height))
	inputs.Move(fyne.NewPos(0, 0))
	arrow.Resize(arrowSize)
	arrow.Move(fyne.NewPos((size.Width-arrowSize.Width)/2, (size.Height-arrowSize.Height)/2))
	outputs.Resize(fyne.NewSize(side, outputs.MinSize().Height))
	outputs.Move(fyne.NewPos(size.Width-side, 0))
}

// txColumns lays out the header and the top row of every card alike, so
// their columns line up: the time column as in the Coins tab, the amount at
// the right, as wide as it needs, and the id taking the rest. Each cell is
// centred vertically.
type txColumns struct{}

func (txColumns) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var width = max(timeColumnWidth(), objects[0].MinSize().Width) + objects[1].MinSize().Width + objects[2].MinSize().Width
	var height = float32(0)
	for _, o := range objects {
		height = max(height, o.MinSize().Height)
	}
	return fyne.NewSize(width, height)
}

func (txColumns) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var timeWidth = max(timeColumnWidth(), objects[0].MinSize().Width)
	var amountWidth = objects[2].MinSize().Width
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
