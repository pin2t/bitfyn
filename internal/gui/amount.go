package gui

import "errors"
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

// balanceText is the main balance line: the spendable balance, which counts
// unconfirmed transactions too.
func balanceText(confirmed, pending int64) string {
	return formatAmount(confirmed + pending)
}

// pendingText is the note next to the balance naming the incoming part of it
// that is still unconfirmed. It is empty when nothing is incoming: pending
// spends lower the balance but get no note.
func pendingText(pending int64) string {
	if pending <= 0 {
		return ""
	}
	return "(" + formatAmount(pending) + " pending)"
}

// Amount units offered where an amount is entered.
const unitSats = "sats"
const unitBTC = "BTC"

// maxSats is the 21 million BTC supply cap in satoshis.
const maxSats = 21_000_000 * satsPerBTC

// parseAmount reads an entered amount in the unit: whole sats, or BTC with
// at most eight decimals. Spaces grouping digits are ignored and an empty
// entry is zero.
func parseAmount(text, unit string) (int64, error) {
	var clean = strings.ReplaceAll(strings.TrimSpace(text), " ", "")
	if clean == "" {
		return 0, nil
	}
	var sats int64
	if unit == unitSats {
		var n, err = strconv.ParseInt(clean, 10, 64)
		if err != nil || n < 0 {
			return 0, errors.New("enter a whole number of sats")
		}
		sats = n
	} else {
		var whole, frac, _ = strings.Cut(clean, ".")
		if len(frac) > 8 {
			return 0, errors.New("BTC amounts have at most 8 decimals")
		}
		var w, err = strconv.ParseInt(whole+"0", 10, 64)
		var f, ferr = strconv.ParseInt(frac+strings.Repeat("0", 8-len(frac)), 10, 64)
		if err != nil || ferr != nil || w < 0 || strings.ContainsAny(clean, "+-") {
			return 0, errors.New("enter an amount like 0.0015")
		}
		sats = w/10*satsPerBTC + f
	}
	if sats > maxSats {
		return 0, errors.New("amount exceeds 21 million BTC")
	}
	return sats, nil
}

// unitText renders sats in the unit, without grouping, for an entry field.
func unitText(sats int64, unit string) string {
	if unit == unitSats {
		return strconv.FormatInt(sats, 10)
	}
	return btcDecimal(sats)
}

// btcDecimal renders sats as a plain BTC decimal with trailing zeros dropped,
// as BIP21 amounts are written.
func btcDecimal(sats int64) string {
	var frac = strings.TrimRight(fmt.Sprintf("%08d", sats%satsPerBTC), "0")
	var whole = strconv.FormatInt(sats/satsPerBTC, 10)
	if frac == "" {
		return whole
	}
	return whole + "." + frac
}

// invoiceURI is the BIP21 payment request for the address, with the amount
// when one is given.
func invoiceURI(address string, sats int64) string {
	var uri = "bitcoin:" + address
	if sats > 0 {
		uri += "?amount=" + btcDecimal(sats)
	}
	return uri
}
