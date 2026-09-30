package gui

import "testing"

// TestFormatAmount checks the sats/BTC switch at 0.05 BTC, digit grouping
// and the trimmed BTC fraction.
func TestFormatAmount(t *testing.T) {
	var cases = []struct {
		sats int64
		want string
	}{
		{0, "0 sats"},
		{999, "999 sats"},
		{1000, "1 000 sats"},
		{1_234_567, "1 234 567 sats"},
		{4_999_999, "4 999 999 sats"},
		{5_000_000, "0.05 BTC"},
		{100_000_000, "1 BTC"},
		{123_445_678_900, "1 234.456789 BTC"},
		{210_000_000_000_000, "2 100 000 BTC"},
		{12_345_678, "0.12345678 BTC"},
		{-1_500, "-1 500 sats"},
		{-250_000_000, "-2.5 BTC"},
	}
	for _, c := range cases {
		if got := formatAmount(c.sats); got != c.want {
			t.Errorf("formatAmount(%d) = %q, want %q", c.sats, got, c.want)
		}
	}
}

// TestBalanceText checks that pending amounts count in the balance and that
// only incoming ones are named in the pending note, without a sign.
func TestBalanceText(t *testing.T) {
	if got, note := balanceText(1_234_567, 0), pendingText(0); got != "1 234 567 sats" || note != "" {
		t.Errorf("no pending: %q, %q", got, note)
	}
	if got, note := balanceText(0, 25_000_000), pendingText(25_000_000); got != "0.25 BTC" || note != "(0.25 BTC pending)" {
		t.Errorf("incoming: %q, %q", got, note)
	}
	if got, note := balanceText(10_000_000, -2_000), pendingText(-2_000); got != "0.09998 BTC" || note != "" {
		t.Errorf("outgoing: %q, %q", got, note)
	}
}

// TestParseAmount checks both units, grouping spaces, empty input and the
// rejected forms.
func TestParseAmount(t *testing.T) {
	var cases = []struct {
		text, unit string
		want       int64
		ok         bool
	}{
		{"", unitSats, 0, true},
		{"1 234 567", unitSats, 1_234_567, true},
		{"12.5", unitSats, 0, false},
		{"-5", unitSats, 0, false},
		{"0.0015", unitBTC, 150_000, true},
		{".5", unitBTC, 50_000_000, true},
		{"1 234.456789", unitBTC, 123_445_678_900, true},
		{"2", unitBTC, 200_000_000, true},
		{"0.000000001", unitBTC, 0, false},
		{"-0.1", unitBTC, 0, false},
		{"abc", unitBTC, 0, false},
		{"21000001", unitBTC, 0, false},
	}
	for _, c := range cases {
		var got, err = parseAmount(c.text, c.unit)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("parseAmount(%q, %s) = %d, %v; want %d ok=%v", c.text, c.unit, got, err, c.want, c.ok)
		}
	}
}

// TestInvoiceURI checks the BIP21 invoice with and without an amount.
func TestInvoiceURI(t *testing.T) {
	if got := invoiceURI("bc1qexample", 0); got != "bitcoin:bc1qexample" {
		t.Errorf("no amount: %q", got)
	}
	if got := invoiceURI("bc1qexample", 150_000); got != "bitcoin:bc1qexample?amount=0.0015" {
		t.Errorf("with amount: %q", got)
	}
	if got := unitText(150_000, unitSats) + " " + unitText(150_000, unitBTC); got != "150000 0.0015" {
		t.Errorf("unitText = %q", got)
	}
}
