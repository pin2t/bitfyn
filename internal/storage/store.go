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
	idx            integer primary key,
	derivationPath text    not null unique,
	address        text    not null unique,
	pubkey         blob    not null,
	used           integer not null default 0,
	createdAt      integer not null
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
create table if not exists peers (
	host      text    not null,
	port      integer not null,
	services  integer not null default 0,
	latencyMs integer not null default 0,
	okCount   integer not null default 0,
	failCount integer not null default 0,
	primary key (host, port)
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

// filterHeaderVersion marks the schema revision that switched the stored
// filter headers from raw filter hashes to chained filter headers. Older rows
// are discarded on open because they cannot be used for linkage verification.
// filterPruneVersion marks the revision that made filterData nullable so a
// downloaded filter can be pruned, keeping only its chained header. The
// cfilters table is rebuilt and rows are dropped because filters are
// re-downloadable from peers and old rows predate the seed-start sync.
const filterHeaderVersion = 2
const filterPruneVersion = 3

func migrate(db *sql.DB) error {
	var _, err = db.Exec(schema)
	if err != nil { return err }
	if err := addPeerColumns(db); err != nil { return err }
	return upgradeSchema(db)
}

// upgradeSchema applies one-time table migrations and stamps the schema
// version, once per database.
func upgradeSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`pragma user_version`).Scan(&version); err != nil {
		return err
	}
	if version < filterHeaderVersion {
		if _, err := db.Exec(`delete from cfilters`); err != nil {
			return err
		}
	}
	if version < filterPruneVersion {
		if err := rebuildCFilters(db); err != nil {
			return err
		}
	}
	if version < filterPruneVersion {
		if _, err := db.Exec(fmt.Sprintf(`pragma user_version = %d`, filterPruneVersion)); err != nil {
			return err
		}
	}
	return nil
}

// rebuildCFilters recreates the cfilters table with a nullable filterData
// column. The old rows are dropped: filter headers are re-downloaded from the
// wallet seed height, so nothing valuable is lost.
func rebuildCFilters(db *sql.DB) error {
	var tx, err = db.Begin()
	if err != nil { return err }
	defer tx.Rollback()
	if _, err := tx.Exec(`drop table if exists cfilters_new`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		create table cfilters_new (
			height       integer primary key,
			blockHash    blob not null,
			filterHeader blob not null,
			filterData   blob
		)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`drop table cfilters`); err != nil { return err }
	if _, err := tx.Exec(`alter table cfilters_new rename to cfilters`); err != nil { return err }
	return tx.Commit()
}

// addPeerColumns adds the peer statistics columns to databases created
// before they existed, keeping old wallets usable.
func addPeerColumns(db *sql.DB) error {
	var existing = make(map[string]bool)
	var rows, err = db.Query(`pragma table_info(peers)`)
	if err != nil { return err }
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name string
		var kind string
		var notnull int
		var def []byte
		var pk int
		if err := rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil { return err }
	for _, column := range []string{
		`services integer not null default 0`,
		`latencyMs integer not null default 0`,
		`okCount integer not null default 0`,
		`failCount integer not null default 0`,
	} {
		var name = strings.SplitN(column, " ", 2)[0]
		if existing[name] { continue }
		if _, err := db.Exec(`alter table peers add column ` + column); err != nil {
			return err
		}
	}
	return nil
}

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

// AddAddress persists a derived address. Idempotent per index.
func (s *Store) AddAddress(index uint32, path, address string, pubkey []byte) error {
	var _, err = s.db.Exec(
		`insert or ignore into addresses (idx, derivationPath, address, pubkey, used, createdAt)
		 values (?, ?, ?, ?, 0, ?)`,
		index, path, address, pubkey, time.Now().Unix(),
	)
	return err
}

// CountAddresses returns the number of derived addresses stored so far.
func (s *Store) CountAddresses() (int, error) {
	var n int
	var err = s.db.QueryRow(`select count(*) from addresses`).Scan(&n)
	return n, err
}
