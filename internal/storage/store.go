// Package storage persists wallet keys and metadata in an SQLite database
// built with SQLCipher (github.com/mutecomm/go-sqlcipher/v4). The cgo
// dependency is deliberate: the same file format is used encrypted or not,
// and a later milestone can set the passphrase from a keyring without any
// schema or code change.
package storage

import "database/sql"
import "errors"
import "fmt"
import "net/url"
import "os"
import "path/filepath"
import "strings"
import "time"
import _ "github.com/mutecomm/go-sqlcipher/v4"

// ErrNoWallet is returned when the database exists but holds no wallet yet.
var ErrNoWallet = errors.New("no wallet stored in database")

const schema = `
CREATE TABLE IF NOT EXISTS meta (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	mnemonic   BLOB    NOT NULL,
	xpub       TEXT    NOT NULL,
	network    TEXT    NOT NULL,
	created_at INTEGER NOT NULL,
	next_index INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS addresses (
	idx             INTEGER PRIMARY KEY,
	derivation_path TEXT    NOT NULL UNIQUE,
	address         TEXT    NOT NULL UNIQUE,
	pubkey          BLOB    NOT NULL,
	used            INTEGER NOT NULL DEFAULT 0,
	created_at      INTEGER NOT NULL
);
`

// Meta is the single wallet metadata row.
type Meta struct {
	Mnemonic  string
	XPub      string
	Network   string
	CreatedAt int64
	NextIndex uint32
}

// Store wraps the wallet database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the wallet database at path. When
// passphrase is non-empty the file is a SQLCipher-encrypted database and the
// passphrase is required for every subsequent open. The key is delivered per
// connection through the driver DSN, so the connection pool stays safe.
func Open(path, passphrase string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	var dsn = path
	if passphrase != "" {
		dsn = fmt.Sprintf("%s?_pragma_key=%s", path, url.QueryEscape(passphrase))
	}
	var db, err = sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		if strings.Contains(strings.ToLower(err.Error()), "file is not a database") {
			return nil, fmt.Errorf("cannot read wallet database (wrong passphrase or corrupt file): %w", err)
		}
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	var _, err = db.Exec(schema)
	return err
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// SaveMeta inserts the wallet metadata row. It is a no-op if a wallet is
// already stored.
func (s *Store) SaveMeta(mnemonic, xpub, network string, createdAt int64) error {
	var _, err = s.db.Exec(
		`INSERT OR IGNORE INTO meta (id, mnemonic, xpub, network, created_at, next_index)
		 VALUES (1, ?, ?, ?, ?, 0)`,
		mnemonic, xpub, network, createdAt,
	)
	return err
}

// Meta returns the wallet metadata row, or ErrNoWallet.
func (s *Store) Meta() (Meta, error) {
	var m Meta
	var err = s.db.QueryRow(
		`SELECT mnemonic, xpub, network, created_at, next_index FROM meta WHERE id = 1`,
	).Scan(&m.Mnemonic, &m.XPub, &m.Network, &m.CreatedAt, &m.NextIndex)
	if errors.Is(err, sql.ErrNoRows) {
		return Meta{}, ErrNoWallet
	}
	if err != nil {
		return Meta{}, err
	}
	return m, nil
}

// UpdateNextIndex records the next derivation index to use.
func (s *Store) UpdateNextIndex(index uint32) error {
	var _, err = s.db.Exec(`UPDATE meta SET next_index = ? WHERE id = 1`, index)
	return err
}

// AddAddress persists a derived address. Idempotent per index.
func (s *Store) AddAddress(index uint32, path, address string, pubkey []byte) error {
	var _, err = s.db.Exec(
		`INSERT OR IGNORE INTO addresses (idx, derivation_path, address, pubkey, used, created_at)
		 VALUES (?, ?, ?, ?, 0, ?)`,
		index, path, address, pubkey, time.Now().Unix(),
	)
	return err
}

// CountAddresses returns the number of derived addresses stored so far.
func (s *Store) CountAddresses() (int, error) {
	var n int
	var err = s.db.QueryRow(`SELECT COUNT(*) FROM addresses`).Scan(&n)
	return n, err
}
