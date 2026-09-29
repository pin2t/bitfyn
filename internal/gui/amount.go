package gui

import "fmt"
import "strconv"
import "strings"

// satsPerBTC is the number of satoshis in one bitcoin.
const satsPerBTC = 100_000_000

// btcThreshold is the amount from which balances are shown in BTC instead of
// sats: 0.05 BTC.
const btcThreshold = 5_000_000

// formatAmount renders an amount of satoshis for display: in sats below
// 0.05 BTC and in BTC otherwise, with the whole part grouped by three digits,
// as in "1 234 567 sats" or "1 234.456789 BTC". Trailing zeros of the BTC
// fraction are dropped.
func formatAmount(sats int64) string {
	var sign = ""
	var abs = uint64(sats)
	if sats < 0 {
		sign = "-"
		abs = uint64(-sats)
	}
	if abs < btcThreshold {
		return sign + groupDigits(strconv.FormatUint(abs, 10)) + " sats"
	}
	var whole = groupDigits(strconv.FormatUint(abs/satsPerBTC, 10))
	var frac = strings.TrimRight(fmt.Sprintf("%08d", abs%satsPerBTC), "0")
	if frac == "" {
		return sign + whole + " BTC"
	}
	return sign + whole + "." + frac + " BTC"
}

// groupDigits separates a string of digits into groups of three from the
// right with spaces.
func groupDigits(digits string) string {
	var b strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(d)
	}
	return b.String()
}

// balanceText is the balance line: the confirmed balance, followed by the
// change unconfirmed transactions make to it, when there is one.
func balanceText(confirmed, pending int64) string {
	var text = formatAmount(confirmed)
	switch {
	case pending > 0:
		text += " (+" + formatAmount(pending) + " pending)"
	case pending < 0:
		text += " (" + formatAmount(pending) + " pending)"
	}
	return text
}
