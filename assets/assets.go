// Package assets holds the data files embedded into the bitfyn binary.
package assets

import _ "embed"

// RatesCSV is the historical BTC/USD rate table with a "ts,cents" header:
// unix time and the price of one bitcoin in US cents.
//
//go:embed rates.csv
var RatesCSV []byte
