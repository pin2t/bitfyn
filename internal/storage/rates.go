package storage

import "database/sql"
import "errors"

// ErrNoRate is returned when the rates table is empty.
var ErrNoRate = errors.New("no bitcoin rate stored")

// Rate is the price of one bitcoin in US cents at a unix time.
type Rate struct {
	Time  int64
	Cents int64
}

// SeedRates fills the rates table with the given rates, but only while the
// table is empty, so rates recorded since are never overwritten. It reports
// whether the rates were inserted.
func (s *Store) SeedRates(rates []Rate) (bool, error) {
	var tx, err = s.db.Begin()
	if err != nil { return false, err }
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRow(`select exists(select 1 from rates)`).Scan(&exists); err != nil {
		return false, err
	}
	if exists { return false, nil }
	stmt, err := tx.Prepare(`insert or replace into rates (ts, cents) values (?, ?)`)
	if err != nil { return false, err }
	defer stmt.Close()
	for _, r := range rates {
		if _, err := stmt.Exec(r.Time, r.Cents); err != nil { return false, err }
	}
	return true, tx.Commit()
}

// AddRate records a rate, replacing one stored for the same time.
func (s *Store) AddRate(r Rate) error {
	var _, err = s.db.Exec(`insert or replace into rates (ts, cents) values (?, ?)`, r.Time, r.Cents)
	return err
}

// LatestRate returns the most recent rate, or ErrNoRate.
func (s *Store) LatestRate() (Rate, error) {
	var r Rate
	var err = s.db.QueryRow(`select ts, cents from rates order by ts desc limit 1`).Scan(&r.Time, &r.Cents)
	if errors.Is(err, sql.ErrNoRows) {
		return Rate{}, ErrNoRate
	}
	return r, err
}
