package gui

import "errors"
import "fmt"
import "strconv"
import "strings"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/container"
import "fyne.io/fyne/v2/dialog"
import "fyne.io/fyne/v2/layout"
import "fyne.io/fyne/v2/widget"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "bitfyn/internal/sync"
import "bitfyn/internal/wallet"

// defaultFeeRate is the fee rate the Send form starts with, in sat/vB. The
// wallet has no fee estimator yet, so the user adjusts it.
const defaultFeeRate = 2

// sendRequest is a filled-in Send form. Empty coins lets the wallet pick
// them; otherwise exactly those coins are spent.
type sendRequest struct {
	address string
	amount  int64
	feeRate int64
	coins   []wallet.Coin
}

// changeAddress is the change address a payment would use; stored reports
// whether it is already in the database.
type changeAddress struct {
	index   uint32
	address string
	path    string
	pubkey  []byte
	script  []byte
	stored  bool
}

// plannedSend is a checked payment ready to sign.
type plannedSend struct {
	address string
	dest    []byte
	spend   wallet.Spend
	feeRate int64
	change  changeAddress
}

// nextChange returns the first stored change address that has not received
// anything yet, or else derives the next one without storing it: planning a
// payment must not leave addresses behind.
func (g *gui) nextChange() (changeAddress, error) {
	var stored, err = g.store.ChangeAddresses()
	if err != nil {
		return changeAddress{}, fmt.Errorf("load change addresses: %w", err)
	}
	for _, a := range stored {
		if sync.IsUsed(a.Address) { continue }
		var script, err = g.wallet.AddressScript(a.Address)
		if err != nil { return changeAddress{}, err }
		return changeAddress{index: a.Index, address: a.Address, path: a.Path, pubkey: a.Pubkey, script: script, stored: true}, nil
	}
	var index = uint32(len(stored))
	var address, path, pubkey, aerr = g.wallet.DeriveChangeAddress(index)
	if aerr != nil { return changeAddress{}, aerr }
	var script, serr = g.wallet.AddressScript(address)
	if serr != nil { return changeAddress{}, serr }
	return changeAddress{index: index, address: address, path: path, pubkey: pubkey, script: script}, nil
}

// planSend checks the request and plans the transaction.
func (g *gui) planSend(r sendRequest) (plannedSend, error) {
	if strings.TrimSpace(r.address) == "" {
		return plannedSend{}, errors.New("enter the destination address")
	}
	var dest, err = g.wallet.AddressScript(r.address)
	if err != nil { return plannedSend{}, err }
	if r.amount <= 0 {
		return plannedSend{}, errors.New("enter an amount")
	}
	var change, cerr = g.nextChange()
	if cerr != nil { return plannedSend{}, cerr }
	var coins = r.coins
	var useAll = len(coins) > 0
	if !useAll {
		coins = sync.Coins()
	}
	var spend, serr = wallet.PlanSpend(coins, useAll, dest, r.amount, r.feeRate, change.script)
	if errors.Is(serr, wallet.ErrInsufficientFunds) && useAll {
		serr = errors.New("the selected coins do not cover the amount and the fee")
	}
	if serr != nil { return plannedSend{}, serr }
	return plannedSend{address: strings.TrimSpace(r.address), dest: dest, spend: spend, feeRate: r.feeRate, change: change}, nil
}

// sendPlanned stores and watches the change address, signs the transaction
// and broadcasts it. It returns the transaction id and how many peers it
// was sent to.
func (g *gui) sendPlanned(p plannedSend) (chainhash.Hash, int, error) {
	if p.spend.Change > 0 {
		if !p.change.stored {
			if err := g.store.AddChangeAddress(p.change.index, p.change.path, p.change.address, p.change.pubkey); err != nil {
				return chainhash.Hash{}, 0, fmt.Errorf("store change address: %w", err)
			}
		}
		sync.Watch(p.change.address, p.change.path, p.change.pubkey)
	}
	var tx, err = g.wallet.SignSpend(p.spend, p.dest, p.change.script)
	if err != nil { return chainhash.Hash{}, 0, err }
	var peers, perr = sync.Broadcast(tx)
	if perr != nil { return chainhash.Hash{}, 0, perr }
	return tx.TxHash(), peers, nil
}

// feeText describes the fee of a planned payment with its size and rate.
func feeText(p plannedSend) string {
	var outputs = [][]byte{p.dest}
	if p.spend.Change > 0 {
		outputs = append(outputs, p.change.script)
	}
	var vsize = wallet.EstimateVSize(len(p.spend.Inputs), outputs)
	return fmt.Sprintf("%s (%d vB at %d sat/vB)", formatAmount(p.spend.Fee), vsize, p.feeRate)
}

// confirmText is the summary shown before a payment is signed.
func confirmText(p plannedSend) string {
	var coins = "1 coin"
	if len(p.spend.Inputs) != 1 {
		coins = fmt.Sprintf("%d coins", len(p.spend.Inputs))
	}
	var text = fmt.Sprintf("Pay %s to\n%s\n\nFee: %s\nTotal: %s\nSpending %s",
		formatAmount(p.spend.Amount), p.address, feeText(p), formatAmount(p.spend.Amount+p.spend.Fee), coins)
	if p.spend.Change > 0 {
		text += fmt.Sprintf(", %s back as change", formatAmount(p.spend.Change))
	}
	return text
}

// coinLabel names a coin in the coin list: value, address and whether it
// is confirmed.
func coinLabel(c wallet.Coin) string {
	var label = formatAmount(c.Value) + " · " + shortAddress(c.Address)
	if !c.Confirmed {
		label += " · unconfirmed"
	}
	return label + " · " + c.OutPoint.String()[:8]
}

func shortAddress(address string) string {
	if len(address) <= 20 { return address }
	return address[:12] + "…" + address[len(address)-6:]
}

// sendForm is the Send dialog content.
type sendForm struct {
	g        *gui
	address  *widget.Entry
	amount   *widget.Entry
	unit     *widget.Select
	lastUnit string
	feeRate  *widget.Entry
	coins    *widget.CheckGroup
	byLabel  map[string]wallet.Coin
	summary  *widget.Label
}

// newSendForm builds the form with the current spendable coins.
func (g *gui) newSendForm() *sendForm {
	var f = &sendForm{g: g, byLabel: make(map[string]wallet.Coin), lastUnit: unitSats}
	f.address = widget.NewEntry()
	f.address.SetPlaceHolder("Destination address")
	f.amount = widget.NewEntry()
	f.amount.SetPlaceHolder("Amount")
	f.unit = widget.NewSelect([]string{unitSats, unitBTC}, nil)
	f.unit.SetSelected(unitSats)
	f.feeRate = widget.NewEntry()
	f.feeRate.SetText(strconv.Itoa(defaultFeeRate))
	var labels []string
	for _, c := range sync.Coins() {
		var label = coinLabel(c)
		f.byLabel[label] = c
		labels = append(labels, label)
	}
	f.coins = widget.NewCheckGroup(labels, func([]string) { f.update() })
	f.summary = widget.NewLabel("")
	f.summary.Wrapping = fyne.TextWrapWord
	f.address.OnChanged = func(string) { f.update() }
	f.amount.OnChanged = func(string) { f.update() }
	f.feeRate.OnChanged = func(string) { f.update() }
	f.unit.OnChanged = func(unit string) { f.convertUnit(unit) }
	return f
}

// request reads the form.
func (f *sendForm) request() (sendRequest, error) {
	var amount, err = parseAmount(f.amount.Text, f.unit.Selected)
	if err != nil { return sendRequest{}, err }
	var rate, rerr = strconv.ParseInt(strings.TrimSpace(f.feeRate.Text), 10, 64)
	if rerr != nil || rate < 1 {
		return sendRequest{}, errors.New("enter a fee rate of at least 1 sat/vB")
	}
	var chosen []wallet.Coin
	for _, label := range f.coins.Selected {
		chosen = append(chosen, f.byLabel[label])
	}
	return sendRequest{address: f.address.Text, amount: amount, feeRate: rate, coins: chosen}, nil
}

// update shows the fee and change of the payment as entered, or what is
// missing or wrong. An untouched form gets a plain hint.
func (f *sendForm) update() {
	if strings.TrimSpace(f.address.Text) == "" && strings.TrimSpace(f.amount.Text) == "" {
		f.summary.Importance = widget.MediumImportance
		f.summary.SetText("Enter the destination address and the amount.")
		return
	}
	var r, err = f.request()
	if err == nil {
		var p plannedSend
		p, err = f.g.planSend(r)
		if err == nil {
			var text = "Fee: " + feeText(p)
			if p.spend.Change > 0 {
				text += "\nChange: " + formatAmount(p.spend.Change)
			}
			f.summary.Importance = widget.MediumImportance
			f.summary.SetText(text)
			return
		}
	}
	f.summary.Importance = widget.DangerImportance
	f.summary.SetText(err.Error())
}

// convertUnit rewrites the entered amount in the newly chosen unit.
func (f *sendForm) convertUnit(unit string) {
	var sats, err = parseAmount(f.amount.Text, f.lastUnit)
	f.lastUnit = unit
	if err == nil && sats > 0 {
		f.amount.SetText(unitText(sats, unit))
	}
	f.update()
}

// fillMax enters the most the chosen coins, or all coins, can pay at the
// entered fee rate.
func (f *sendForm) fillMax() {
	var rate, err = strconv.ParseInt(strings.TrimSpace(f.feeRate.Text), 10, 64)
	if err != nil || rate < 1 {
		rate = defaultFeeRate
	}
	var coins []wallet.Coin
	for _, label := range f.coins.Selected {
		coins = append(coins, f.byLabel[label])
	}
	if len(coins) == 0 {
		coins = sync.Coins()
	}
	var dest, derr = f.g.wallet.AddressScript(f.address.Text)
	if derr != nil {
		dest = make([]byte, 22)
	}
	f.amount.SetText(unitText(wallet.MaxAmount(coins, dest, rate), f.unit.Selected))
}

// showSend opens the Send dialog. Review plans the payment and asks for
// confirmation before signing and broadcasting it.
func (g *gui) showSend() { g.openSend() }

// openSend opens the Send dialog and returns its form.
func (g *gui) openSend() *sendForm {
	var f = g.newSendForm()
	var d *dialog.CustomDialog
	var maxBtn = widget.NewButton("Max", f.fillMax)
	var review = widget.NewButton("Review", func() {
		var r, err = f.request()
		var p plannedSend
		if err == nil {
			p, err = g.planSend(r)
		}
		if err != nil {
			dialog.ShowError(err, g.window)
			return
		}
		dialog.ShowCustomConfirm("Confirm payment", "Sign & send", "Back", widget.NewLabel(confirmText(p)), func(ok bool) {
			if !ok { return }
			var txid, peers, err = g.sendPlanned(p)
			if err != nil {
				dialog.ShowError(fmt.Errorf("payment not sent: %w", err), g.window)
				return
			}
			d.Hide()
			dialog.ShowInformation("Payment sent", fmt.Sprintf("Transaction %s\nbroadcast to %d peers.", txid, peers), g.window)
		}, g.window)
	})
	review.Importance = widget.HighImportance
	var cancel = widget.NewButton("Cancel", func() { d.Hide() })
	var coinsLabel = widget.NewLabel("Coins to spend (none ticked: chosen automatically)")
	var coinList = container.NewVScroll(f.coins)
	coinList.SetMinSize(fyne.NewSize(0, 140))
	var form = container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("To", f.address),
			widget.NewFormItem("Amount", container.NewBorder(nil, nil, nil, container.NewHBox(f.unit, maxBtn), f.amount)),
			widget.NewFormItem("Fee rate", container.NewBorder(nil, nil, nil, widget.NewLabel("sat/vB"), f.feeRate)),
		),
		coinsLabel,
		coinList,
		f.summary,
		container.NewHBox(cancel, layout.NewSpacer(), review),
	)
	d = dialog.NewCustomWithoutButtons("Send", form, g.window)
	d.Resize(fyne.NewSize(620, 560))
	f.update()
	d.Show()
	return f
}
