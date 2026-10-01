package rates

import "bytes"
import "context"
import "fmt"
import "net/http"
import "net/http/httptest"
import "path/filepath"
import "strings"
import "testing"
import "time"
import "bitfyn/assets"
import "bitfyn/internal/storage"

// TestEmbeddedCSV checks that the embedded history parses and is ordered.
func TestEmbeddedCSV(t *testing.T) {
	var rates, err = parseCSV(bytes.NewReader(assets.RatesCSV))
	if err != nil { t.Fatalf("parseCSV: %v", err) }
	if len(rates) < 1000 { t.Fatalf("only %d rates embedded", len(rates)) }
	for i := 1; i < len(rates); i++ {
		if rates[i].Time <= rates[i-1].Time {
			t.Fatalf("rate %d at %d not after %d", i, rates[i].Time, rates[i-1].Time)
		}
	}
}

func TestParseCSVErrors(t *testing.T) {
	for _, in := range []string{"", "time,usd\n1,2\n", "ts,cents\n1,x\n", "ts,cents\n1,0\n", "ts,cents\n1\n"} {
		if _, err := parseCSV(strings.NewReader(in)); err == nil {
			t.Errorf("parseCSV(%q) succeeded", in)
		}
	}
}

// TestCombine checks that close rates are averaged, rounding half up, even
// at a spread of exactly 10%, and that the highest rate is taken once the
// lowest is more than 10% below it.
func TestCombine(t *testing.T) {
	var cases = []struct {
		in   []int64
		want int64
	}{
		{[]int64{100, 101, 103}, 101},
		{[]int64{100, 101, 102}, 101},
		{[]int64{90, 95, 100}, 95},
		{[]int64{89, 95, 100}, 100},
		{[]int64{100, 100, 50}, 100},
		{[]int64{6500012}, 6500012},
		{[]int64{3, 4}, 4},
	}
	for _, c := range cases {
		if got := combine(c.in); got != c.want {
			t.Errorf("combine(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParsers(t *testing.T) {
	var cases = []struct {
		parse func([]byte) (string, error)
		body  string
		want  int64
	}{
		{parseCoinbase, `{"data":{"amount":"65000.126","base":"BTC","currency":"USD"}}`, 6500013},
		{parseKraken, `{"error":[],"result":{"XXBTZUSD":{"a":["1"],"c":["64999.9","0.1"]}}}`, 6499990},
		{parseBitstamp, `{"last":"65001","bid":"65000"}`, 6500100},
	}
	for _, c := range cases {
		var price, err = c.parse([]byte(c.body))
		if err != nil { t.Fatalf("parse %s: %v", c.body, err) }
		var cents, cerr = toCents(price)
		if cerr != nil || cents != c.want {
			t.Errorf("parse %s = %d, %v; want %d", c.body, cents, cerr, c.want)
		}
	}
	if _, err := parseKraken([]byte(`{"error":["EQuery:Unknown asset pair"]}`)); err == nil {
		t.Error("parseKraken accepted an error response")
	}
	if _, err := toCents(""); err == nil {
		t.Error("toCents accepted an empty price")
	}
}

// fakeSources points the sources at local servers quoting the given prices,
// a negative price making the server fail, and restores them after the test.
func fakeSources(t *testing.T, prices ...float64) {
	var saved = sources
	t.Cleanup(func() { sources = saved })
	sources = nil
	for i, p := range prices {
		var srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p < 0 {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, `{"last":"%.2f"}`, p)
		}))
		t.Cleanup(srv.Close)
		sources = append(sources, source{fmt.Sprintf("fake%d", i), srv.URL, parseBitstamp})
	}
}

func openStore(t *testing.T) *storage.Store {
	var s, err = storage.Open(filepath.Join(t.TempDir(), "w.db"), "")
	if err != nil { t.Fatalf("Open: %v", err) }
	t.Cleanup(func() { s.Close() })
	return s
}

// TestRefresh checks that a fresh rate is left alone, and that an old one is
// replaced by the combination of the sources that answered.
func TestRefresh(t *testing.T) {
	var db = openStore(t)
	var now = time.Unix(1_800_000_000, 0)
	fakeSources(t, 60000, 60100, 60200)
	if err := db.AddRate(storage.Rate{Time: now.Add(-29 * time.Minute).Unix(), Cents: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok := refresh(context.Background(), db, now); ok {
		t.Fatal("refresh reported a rate stored for a fresh one")
	}
	if r, _ := db.LatestRate(); r.Cents != 1 {
		t.Fatalf("rate 29 minutes old was refreshed: %+v", r)
	}
	var want = storage.Rate{Time: now.Add(time.Minute).Unix(), Cents: 6010000}
	if r, ok := refresh(context.Background(), db, now.Add(time.Minute)); !ok || r != want {
		t.Fatalf("refresh returned %+v, %v; want %+v, true", r, ok, want)
	}
	if r, _ := db.LatestRate(); r != want {
		t.Fatalf("rate 30 minutes old refreshed to %+v, want the average 6010000", r)
	}
	fakeSources(t, 50000, -1, 60000)
	refresh(context.Background(), db, now.Add(time.Hour))
	if r, _ := db.LatestRate(); r.Cents != 6000000 {
		t.Fatalf("spread rates stored %d, want the highest 6000000", r.Cents)
	}
	fakeSources(t, -1, -1, -1)
	refresh(context.Background(), db, now.Add(2*time.Hour))
	if r, _ := db.LatestRate(); r.Time != now.Add(time.Hour).Unix() {
		t.Fatalf("rate stored with every source down: %+v", r)
	}
}

// TestSeed checks that the embedded history is loaded into an empty table
// only once.
func TestSeed(t *testing.T) {
	var db = openStore(t)
	if err := Seed(db); err != nil { t.Fatalf("Seed: %v", err) }
	var first, err = db.LatestRate()
	if err != nil { t.Fatalf("LatestRate: %v", err) }
	var newer = storage.Rate{Time: first.Time + 1, Cents: 42}
	if err := db.AddRate(newer); err != nil { t.Fatal(err) }
	if err := Seed(db); err != nil { t.Fatalf("Seed again: %v", err) }
	if r, _ := db.LatestRate(); r != newer {
		t.Fatalf("latest rate after second Seed = %+v, want %+v", r, newer)
	}
}
