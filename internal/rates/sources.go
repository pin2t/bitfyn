package rates

import "context"
import "encoding/json"
import "errors"
import "fmt"
import "io"
import "log"
import "math"
import "net/http"
import "strconv"
import "strings"
import "time"

// fetchTimeout bounds one request to a rate source.
const fetchTimeout = 20 * time.Second

// source is a public API quoting the BTC/USD price.
type source struct {
	name  string
	url   string
	parse func(body []byte) (string, error)
}

// sources are the exchanges asked for the current rate, all at once.
var sources = []source{
	{"coinbase", "https://api.coinbase.com/v2/prices/BTC-USD/spot", parseCoinbase},
	{"kraken", "https://api.kraken.com/0/public/Ticker?pair=XBTUSD", parseKraken},
	{"bitstamp", "https://www.bitstamp.net/api/v2/ticker/btcusd/", parseBitstamp},
}

// fetchRate asks every source in parallel and combines the rates of those
// that answered. It fails only when no source answered.
func fetchRate(ctx context.Context) (int64, error) {
	type result struct {
		name  string
		cents int64
		err   error
	}
	var results = make(chan result, len(sources))
	for _, s := range sources {
		go func() {
			var cents, err = fetchSource(ctx, s)
			results <- result{s.name, cents, err}
		}()
	}
	var rates []int64
	var errs []error
	for range sources {
		var r = <-results
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.name, r.err))
			continue
		}
		rates = append(rates, r.cents)
	}
	if len(rates) == 0 {
		return 0, fmt.Errorf("no rate source answered: %w", errors.Join(errs...))
	}
	if len(errs) > 0 {
		log.Printf("rates: using %d of %d sources: %v", len(rates), len(sources), errors.Join(errs...))
	}
	return combine(rates), nil
}

// fetchSource gets the price quoted by one source in US cents.
func fetchSource(ctx context.Context, s source) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	var req, err = http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil { return 0, err }
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil { return 0, err }
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil { return 0, err }
	price, err := s.parse(body)
	if err != nil { return 0, err }
	return toCents(price)
}

// toCents converts a decimal USD price to whole cents, rounding.
func toCents(price string) (int64, error) {
	var usd, err = strconv.ParseFloat(strings.TrimSpace(price), 64)
	if err != nil || usd <= 0 || math.IsInf(usd, 0) {
		return 0, fmt.Errorf("bad price %q", price)
	}
	return int64(math.Round(usd * 100)), nil
}

// parseCoinbase reads {"data":{"amount":"65000.12",...}}.
func parseCoinbase(body []byte) (string, error) {
	var v struct {
		Data struct {
			Amount string `json:"amount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil { return "", err }
	return v.Data.Amount, nil
}

// parseKraken reads the last trade price from
// {"error":[],"result":{"XXBTZUSD":{"c":["65000.1","0.01"],...}}}.
func parseKraken(body []byte) (string, error) {
	var v struct {
		Error  []string `json:"error"`
		Result map[string]struct {
			Last []string `json:"c"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &v); err != nil { return "", err }
	if len(v.Error) > 0 {
		return "", errors.New(strings.Join(v.Error, "; "))
	}
	for _, ticker := range v.Result {
		if len(ticker.Last) > 0 { return ticker.Last[0], nil }
	}
	return "", errors.New("no ticker in response")
}

// parseBitstamp reads {"last":"65000",...}.
func parseBitstamp(body []byte) (string, error) {
	var v struct {
		Last string `json:"last"`
	}
	if err := json.Unmarshal(body, &v); err != nil { return "", err }
	return v.Last, nil
}
