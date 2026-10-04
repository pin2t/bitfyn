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
create table if not exists meta (
	id        integer primary key check (id = 1),
	mnemonic  blob    not null,
	xpub      text    not null,
	network   text    not null,
	createdAt integer not null,
	nextIndex integer not null default 0
);
create table if not exists addresses (
	type           text    not null check (type in ('receive', 'change')),
	idx            integer not null,
	derivationPath text    not null unique,
	address        text    not null unique,
	pubkey         blob    not null,
	createdAt      integer not null,
	primary key (type, idx)
);
create table if not exists headers (
	height     integer primary key,
	hash       blob not null unique,
	prevHash   blob not null,
	merkleRoot blob not null,
	version    integer not null,
	timestamp  integer not null,
	bits       integer not null,
	nonce      integer not null
);
create table if not exists cfilters (
	height       integer primary key,
	blockHash    blob not null,
	filterHeader blob not null,
	filterData   blob
);
create table if not exists matches (
	height    integer not null,
	blockHash blob    not null,
	address   text    not null,
	script    blob    not null,
	primary key (height, address)
);
create table if not exists transactions (
	txid      blob    primary key,
	height    integer not null,
	blockHash blob    not null,
	raw       blob    not null
);
create table if not exists pending (
	txid   blob    primary key,
	raw    blob    not null,
	seenAt integer not null
);
create table if not exists peers (
	host      text    not null,
	port      integer not null,
	services  integer not null default 0,
	latencyMs integer not null default 0,
	okCount   integer not null default 0,
	failCount integer not null default 0,
	primary key (host, port)
);
create table if not exists rates (
	ts    integer primary key,
	cents integer not null
);
create table if not exists rescans (
	address    text    primary key,
	fromHeight integer not null
);
create table if not exists settings (
	key   text primary key,
	value text not null
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
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		if strings.Contains(strings.ToLower(err.Error()), "file is not a database") {
			return nil, fmt.Errorf("cannot read wallet database (wrong passphrase or corrupt file): %w", err)
		}
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

// The address types: receive addresses are shown to be paid to, change
// addresses take the change of the wallet's own payments.
const addressReceive = "receive"
const addressChange = "change"

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// SaveMeta inserts the wallet metadata row. It is a no-op if a wallet is
// already stored.
func (s *Store) SaveMeta(mnemonic, xpub, network string, createdAt int64) error {
	var _, err = s.db.Exec(
		`insert or ignore into meta (id, mnemonic, xpub, network, createdAt, nextIndex)
		 values (1, ?, ?, ?, ?, 0)`,
		mnemonic, xpub, network, createdAt,
	)
	return err
}

// Meta returns the wallet metadata row, or ErrNoWallet.
func (s *Store) Meta() (Meta, error) {
	var m Meta
	var err = s.db.QueryRow(
		`select mnemonic, xpub, network, createdAt, nextIndex from meta where id = 1`,
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
	var _, err = s.db.Exec(`update meta set nextIndex = ? where id = 1`, index)
	return err
}

// AddAddress persists a derived receive address. Idempotent per index.
func (s *Store) AddAddress(index uint32, path, address string, pubkey []byte) error {
	return s.addAddress(addressReceive, index, path, address, pubkey)
}

// addAddress persists a derived address of the type. Idempotent per type and
// index.
func (s *Store) addAddress(kind string, index uint32, path, address string, pubkey []byte) error {
	var _, err = s.db.Exec(
		`insert or ignore into addresses (type, idx, derivationPath, address, pubkey, createdAt)
		 values (?, ?, ?, ?, ?, ?)`,
		kind, index, path, address, pubkey, time.Now().Unix(),
	)
	return err
}

// CountAddresses returns the number of derived receive addresses stored so
// far.
func (s *Store) CountAddresses() (int, error) {
	var n int
	var err = s.db.QueryRow(`select count(*) from addresses where type = ?`, addressReceive).Scan(&n)
	return n, err
}
