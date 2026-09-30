package storage

import "errors"
import "path/filepath"
import "testing"

// TestSeedRates checks that seeding fills an empty table only, and that the
// latest rate is the one with the highest time.
func TestSeedRates(t *testing.T) {
	var s, err = Open(filepath.Join(t.TempDir(), "w.db"), "")
	if err != nil { t.Fatalf("Open: %v", err) }
	defer s.Close()
	if _, err := s.LatestRate(); !errors.Is(err, ErrNoRate) {
		t.Fatalf("LatestRate on empty table = %v, want ErrNoRate", err)
	}
	seeded, err := s.SeedRates([]Rate{{100, 7}, {300, 9}, {200, 8}})
	if err != nil || !seeded {
		t.Fatalf("SeedRates = %v, %v; want true, nil", seeded, err)
	}
	seeded, err = s.SeedRates([]Rate{{400, 1}})
	if err != nil || seeded {
		t.Fatalf("SeedRates on filled table = %v, %v; want false, nil", seeded, err)
	}
	latest, err := s.LatestRate()
	if err != nil || latest != (Rate{300, 9}) {
		t.Fatalf("LatestRate = %+v, %v; want {300 9}", latest, err)
	}
	if err := s.AddRate(Rate{500, 12}); err != nil { t.Fatalf("AddRate: %v", err) }
	latest, err = s.LatestRate()
	if err != nil || latest != (Rate{500, 12}) {
		t.Fatalf("LatestRate after AddRate = %+v, %v; want {500 12}", latest, err)
	}
}
