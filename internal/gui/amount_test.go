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

// TestBalanceText checks that pending amounts count in the balance and are
// named in the suffix.
func TestBalanceText(t *testing.T) {
	if got := balanceText(1_234_567, 0); got != "1 234 567 sats" {
		t.Errorf("no pending: %q", got)
	}
	if got := balanceText(0, 25_000_000); got != "0.25 BTC (+0.25 BTC pending)" {
		t.Errorf("incoming: %q", got)
	}
	if got := balanceText(10_000_000, -2_000); got != "0.09998 BTC (-2 000 sats pending)" {
		t.Errorf("outgoing: %q", got)
	}
}
