// Package rates keeps the BTC/USD rate table up to date. The table is seeded
// from the history embedded in the binary, and while the application runs a
// background loop records a fresh rate from public exchange APIs whenever the
// latest stored one gets old.
package rates

import "bytes"
import "context"
import "encoding/csv"
import "errors"
import "fmt"
import "io"
import "log"
import "strconv"
import "sync"
import "time"
import "bitfyn/assets"
import "bitfyn/internal/storage"

// checkInterval is how often the loop looks at the age of the latest rate.
const checkInterval = 10 * time.Minute

// maxAge is the age from which the latest rate is refreshed.
const maxAge = 30 * time.Minute

// maxSpread is the relative distance between the lowest and the highest
// source rate above which the sources are not averaged: the highest rate is
// stored instead.
const maxSpread = 0.10

// Seed fills an empty rates table with the embedded rate history. A table
// that already holds rates is left alone.
func Seed(db *storage.Store) error {
	var history, err = parseCSV(bytes.NewReader(assets.RatesCSV))
	if err != nil { return fmt.Errorf("parse embedded rates: %w", err) }
	var seeded, serr = db.SeedRates(history)
	if serr != nil { return fmt.Errorf("seed rates: %w", serr) }
	if seeded {
		log.Printf("rates: seeded %d historical rates", len(history))
	}
	return nil
}

// parseCSV reads "ts,cents" rows after a header line.
func parseCSV(r io.Reader) ([]storage.Rate, error) {
	var records, err = csv.NewReader(r).ReadAll()
	if err != nil { return nil, err }
	if len(records) == 0 || len(records[0]) != 2 || records[0][0] != "ts" || records[0][1] != "cents" {
		return nil, errors.New(`missing "ts,cents" header`)
	}
	var rates = make([]storage.Rate, 0, len(records)-1)
	for i, rec := range records[1:] {
		var ts, terr = strconv.ParseInt(rec[0], 10, 64)
		var cents, cerr = strconv.ParseInt(rec[1], 10, 64)
		if terr != nil || cerr != nil || cents <= 0 {
			return nil, fmt.Errorf("line %d: bad rate %q", i+2, rec)
		}
		rates = append(rates, storage.Rate{Time: ts, Cents: cents})
	}
	return rates, nil
}

var wg sync.WaitGroup
var cancel context.CancelFunc = func() {}

// Start runs the refresh loop in the background: it checks the latest rate
// right away and then every ten minutes, and fetches a new one when the
// latest is thirty minutes old or more. onRate, when not nil, is called from
// the loop with every rate stored.
func Start(db *storage.Store, onRate func(storage.Rate)) {
	var ctx context.Context
	ctx, cancel = context.WithCancel(context.Background())
	wg.Add(1)
	go func() {
		defer wg.Done()
		var ticker = time.NewTicker(checkInterval)
		defer ticker.Stop()
		for {
			if r, ok := refresh(ctx, db, time.Now()); ok && onRate != nil {
				onRate(r)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Stop ends the refresh loop, aborting a fetch in flight, and waits for it
// to finish so the store can be closed.
func Stop() {
	cancel()
	wg.Wait()
}

// refresh fetches and stores a new rate when the latest stored one is older
// than maxAge at now. It returns the rate and true when one was stored.
func refresh(ctx context.Context, db *storage.Store, now time.Time) (storage.Rate, bool) {
	var latest, err = db.LatestRate()
	if err != nil && !errors.Is(err, storage.ErrNoRate) {
		log.Printf("rates: read latest rate: %v", err)
		return storage.Rate{}, false
	}
	if err == nil && now.Sub(time.Unix(latest.Time, 0)) < maxAge {
		return storage.Rate{}, false
	}
	var cents, centsErr = fetchRate(ctx)
	if centsErr != nil {
		if ctx.Err() == nil {
			log.Printf("rates: %v", centsErr)
		}
		return storage.Rate{}, false
	}
	var rate = storage.Rate{Time: now.Unix(), Cents: cents}
	if err := db.AddRate(rate); err != nil {
		log.Printf("rates: store rate: %v", err)
		return storage.Rate{}, false
	}
	log.Printf("rates: stored %d.%02d USD", cents/100, cents%100)
	return rate, true
}

// combine merges the source rates into the one to store: their average, or
// the highest rate when the lowest is more than maxSpread below it.
func combine(cents []int64) int64 {
	var lo, hi, sum = cents[0], cents[0], int64(0)
	for _, c := range cents {
		lo = min(lo, c)
		hi = max(hi, c)
		sum += c
	}
	if float64(hi-lo) > maxSpread*float64(hi) {
		return hi
	}
	var n = int64(len(cents))
	return (sum + n/2) / n
}
