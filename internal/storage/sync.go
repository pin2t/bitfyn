package storage

import "database/sql"
import "errors"
import "github.com/btcsuite/btcd/chaincfg/chainhash"

// Header is one validated block header kept by the SPV sync.
type Header struct {
	Height     int32
	Hash       chainhash.Hash
	PrevHash   chainhash.Hash
	MerkleRoot chainhash.Hash
	Version    int32
	Timestamp  int64
	Bits       uint32
	Nonce      uint32
}

// Filter is a stored BIP158 basic filter for one block.
type Filter struct {
	Height       int32
	BlockHash    chainhash.Hash
	FilterHeader chainhash.Hash
	Data         []byte
}

// Match records one wallet script that matched a block filter.
type Match struct {
	Height    int32
	BlockHash chainhash.Hash
	Address   string
	Script    []byte
}

// Transaction is one confirmed wallet transaction in its serialized form.
type Transaction struct {
	Txid      chainhash.Hash
	Height    int32
	BlockHash chainhash.Hash
	Raw       []byte
}

// PendingTx is one unconfirmed wallet transaction relayed by a peer, with
// the unix time it was first seen.
type PendingTx struct {
	Txid   chainhash.Hash
	Raw    []byte
	SeenAt int64
}

// StoredAddress is one derived address row with its derivation path and
// public key.
type StoredAddress struct {
	Index   uint32
	Path    string
	Address string
	Pubkey  []byte
}

// Rescan is a queued rescan of the blocks from the height on for a wallet
// address that was watched after blocks paying it were scanned.
type Rescan struct {
	Address string
	From    int32
}

// Peer is one known network peer with its connection statistics.
type Peer struct {
	Host      string
	Port      uint16
	Services  uint64
	LatencyMs int64
	OkCount   int64
	FailCount int64
}

// SaveHeader persists one validated header, replacing any row at the height.
func (s *Store) SaveHeader(h Header) error {
	var hash = h.Hash[:]
	var prev = h.PrevHash[:]
	var root = h.MerkleRoot[:]
	var _, err = s.db.Exec(
		`insert or replace into headers (height, hash, prevHash, merkleRoot, version, timestamp, bits, nonce)
		 values (?, ?, ?, ?, ?, ?, ?, ?)`,
		h.Height, hash, prev, root, h.Version, h.Timestamp, h.Bits, h.Nonce,
	)
	return err
}

// SaveHeaders persists a batch of validated headers in one transaction,
// replacing any rows at the same heights.
func (s *Store) SaveHeaders(headers []Header) error {
	var tx, err = s.db.Begin()
	if err != nil { return err }
	defer tx.Rollback()
	var stmt, serr = tx.Prepare(
		`insert or replace into headers (height, hash, prevHash, merkleRoot, version, timestamp, bits, nonce)
		 values (?, ?, ?, ?, ?, ?, ?, ?)`)
	if serr != nil { return serr }
	defer stmt.Close()
	for _, h := range headers {
		if _, err := stmt.Exec(h.Height, h.Hash[:], h.PrevHash[:], h.MerkleRoot[:], h.Version, h.Timestamp, h.Bits, h.Nonce); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Headers streams every stored header in height order to the callback,
// reading them with a single query. An error from the callback stops the
// iteration and is returned.
func (s *Store) Headers(fn func(Header) error) error {
	var rows, err = s.db.Query(`select height, hash, prevHash, merkleRoot, version, timestamp, bits, nonce from headers order by height`)
	if err != nil { return err }
	defer rows.Close()
	for rows.Next() {
		var h Header
		var hash []byte
		var prev []byte
		var root []byte
		if err := rows.Scan(&h.Height, &hash, &prev, &root, &h.Version, &h.Timestamp, &h.Bits, &h.Nonce); err != nil {
			return err
		}
		copy(h.Hash[:], hash)
		copy(h.PrevHash[:], prev)
		copy(h.MerkleRoot[:], root)
		if err := fn(h); err != nil { return err }
	}
	return rows.Err()
}

// HeaderAt returns the stored header at the height, if any.
func (s *Store) HeaderAt(height int32) (Header, bool, error) {
	var h Header
	var hash []byte
	var prev []byte
	var root []byte
	var err = s.db.QueryRow(
		`select height, hash, prevHash, merkleRoot, version, timestamp, bits, nonce from headers where height = ?`,
		height,
	).Scan(&h.Height, &hash, &prev, &root, &h.Version, &h.Timestamp, &h.Bits, &h.Nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return Header{}, false, nil
	}
	if err != nil {
		return Header{}, false, err
	}
	copy(h.Hash[:], hash)
	copy(h.PrevHash[:], prev)
	copy(h.MerkleRoot[:], root)
	return h, true, nil
}

// HeaderCount returns the number of stored headers.
func (s *Store) HeaderCount() (int32, error) {
	var n int64
	var err = s.db.QueryRow(`select count(*) from headers`).Scan(&n)
	return int32(n), err
}

// DeleteHeadersFrom removes every stored header above the given height.
func (s *Store) DeleteHeadersFrom(height int32) error {
	var _, err = s.db.Exec(`delete from headers where height > ?`, height)
	return err
}

// DeleteFiltersFrom removes every stored filter row above the given height.
// It keeps the header chain and filter rows consistent when a shallow fork is
// rewound.
func (s *Store) DeleteFiltersFrom(height int32) error {
	var _, err = s.db.Exec(`delete from cfilters where height > ?`, height)
	return err
}

// SaveFilter persists one verified BIP158 basic filter. When Data is nil the
// row stores only the chained header, which is the pruned state.
func (s *Store) SaveFilter(f Filter) error {
	var _, err = s.db.Exec(
		`insert or replace into cfilters (height, blockHash, filterHeader, filterData) values (?, ?, ?, ?)`,
		f.Height, f.BlockHash[:], f.FilterHeader[:], f.Data,
	)
	return err
}

// SaveFilters persists a batch of verified filter rows in one transaction.
func (s *Store) SaveFilters(filters []Filter) error {
	var tx, err = s.db.Begin()
	if err != nil { return err }
	defer tx.Rollback()
	var stmt, stmtErr = tx.Prepare(`insert or replace into cfilters (height, blockHash, filterHeader, filterData) values (?, ?, ?, ?)`)
	if stmtErr != nil { return stmtErr }
	defer stmt.Close()
	for _, f := range filters {
		if _, err := stmt.Exec(f.Height, f.BlockHash[:], f.FilterHeader[:], f.Data); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneFilterData drops the filter data at the given height, leaving only the
// chained header that links the filter header chain.
func (s *Store) PruneFilterData(height int32) error {
	var _, err = s.db.Exec(`update cfilters set filterData = null where height = ?`, height)
	return err
}

// PruneFilterDataFrom drops any filter data kept at or above the given
// height. It cleans up rows that were downloaded but not pruned before a
// crash; synced rows keep their chained headers.
func (s *Store) PruneFilterDataFrom(height int32) error {
	var _, err = s.db.Exec(`update cfilters set filterData = null where height >= ? and filterData is not null`, height)
	return err
}

// FilterResumeHeight returns the height at which the next filter download
// must start: one past the highest stored filter row at or after from. Stored
// rows are contiguous from the wallet seed height, so the first missing
// height is the resume point.
func (s *Store) FilterResumeHeight(from int32) (int32, error) {
	var highest sql.NullInt64
	var err = s.db.QueryRow(`select max(height) from cfilters where height >= ?`, from).Scan(&highest)
	if err != nil {
		return 0, err
	}
	if !highest.Valid || int32(highest.Int64) < from {
		return from, nil
	}
	return int32(highest.Int64) + 1, nil
}

// FilterHeaderAt returns the stored filter header at the height, if any.
func (s *Store) FilterHeaderAt(height int32) (chainhash.Hash, bool, error) {
	var raw []byte
	var err = s.db.QueryRow(`select filterHeader from cfilters where height = ?`, height).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return chainhash.Hash{}, false, nil
	}
	if err != nil {
		return chainhash.Hash{}, false, err
	}
	var h chainhash.Hash
	copy(h[:], raw)
	return h, true, nil
}

// FilterCount returns the number of stored filters.
func (s *Store) FilterCount() (int32, error) {
	var n int64
	var err = s.db.QueryRow(`select count(*) from cfilters`).Scan(&n)
	return int32(n), err
}

// SaveMatch records a filter hit for a wallet address. Idempotent.
func (s *Store) SaveMatch(m Match) error {
	var _, err = s.db.Exec(
		`insert or ignore into matches (height, blockHash, address, script) values (?, ?, ?, ?)`,
		m.Height, m.BlockHash[:], m.Address, m.Script,
	)
	return err
}

// Matches returns every recorded filter hit.
func (s *Store) Matches() ([]Match, error) {
	var rows, err = s.db.Query(`select height, blockHash, address, script from matches order by height, address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Match
	for rows.Next() {
		var m Match
		var block []byte
		if err := rows.Scan(&m.Height, &block, &m.Address, &m.Script); err != nil {
			return nil, err
		}
		copy(m.BlockHash[:], block)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMatchesFrom removes every filter match above the given height.
func (s *Store) DeleteMatchesFrom(height int32) error {
	var _, err = s.db.Exec(`delete from matches where height > ?`, height)
	return err
}

// SaveTransaction stores a wallet transaction, replacing an earlier copy.
func (s *Store) SaveTransaction(t Transaction) error {
	var _, err = s.db.Exec(
		`insert or replace into transactions (txid, height, blockHash, raw) values (?, ?, ?, ?)`,
		t.Txid[:], t.Height, t.BlockHash[:], t.Raw,
	)
	return err
}

// Transactions returns every stored wallet transaction in block order.
func (s *Store) Transactions() ([]Transaction, error) {
	var rows, err = s.db.Query(`select txid, height, blockHash, raw from transactions order by height, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transaction
	for rows.Next() {
		var t Transaction
		var txid []byte
		var block []byte
		if err := rows.Scan(&txid, &t.Height, &block, &t.Raw); err != nil {
			return nil, err
		}
		copy(t.Txid[:], txid)
		copy(t.BlockHash[:], block)
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTransactionsFrom removes every wallet transaction confirmed above
// the given height.
func (s *Store) DeleteTransactionsFrom(height int32) error {
	var _, err = s.db.Exec(`delete from transactions where height > ?`, height)
	return err
}

// SavePending stores an unconfirmed wallet transaction. A transaction seen
// before keeps its first-seen time.
func (s *Store) SavePending(p PendingTx) error {
	var _, err = s.db.Exec(`insert or ignore into pending (txid, raw, seenAt) values (?, ?, ?)`, p.Txid[:], p.Raw, p.SeenAt)
	return err
}

// PendingTransactions returns every unconfirmed wallet transaction, oldest
// first.
func (s *Store) PendingTransactions() ([]PendingTx, error) {
	var rows, err = s.db.Query(`select txid, raw, seenAt from pending order by seenAt, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingTx
	for rows.Next() {
		var p PendingTx
		var txid []byte
		if err := rows.Scan(&txid, &p.Raw, &p.SeenAt); err != nil {
			return nil, err
		}
		copy(p.Txid[:], txid)
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePending removes an unconfirmed transaction, once it confirmed or
// was replaced.
func (s *Store) DeletePending(txid chainhash.Hash) error {
	var _, err = s.db.Exec(`delete from pending where txid = ?`, txid[:])
	return err
}

// DeletePendingBefore removes the unconfirmed transactions first seen before
// the unix time and returns how many were removed.
func (s *Store) DeletePendingBefore(unix int64) (int64, error) {
	var res, err = s.db.Exec(`delete from pending where seenAt < ?`, unix)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Addresses returns all derived receive addresses.
func (s *Store) Addresses() ([]StoredAddress, error) {
	return s.addresses(addressReceive)
}

// ChangeAddresses returns all derived change addresses.
func (s *Store) ChangeAddresses() ([]StoredAddress, error) {
	return s.addresses(addressChange)
}

// addresses returns the derived addresses of the type in index order.
func (s *Store) addresses(kind string) ([]StoredAddress, error) {
	var rows, err = s.db.Query(`select idx, derivationPath, address, pubkey from addresses where type = ? order by idx`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredAddress
	for rows.Next() {
		var a StoredAddress
		if err := rows.Scan(&a.Index, &a.Path, &a.Address, &a.Pubkey); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddChangeAddress records a derived change address. Idempotent.
func (s *Store) AddChangeAddress(index uint32, path, address string, pubkey []byte) error {
	return s.addAddress(addressChange, index, path, address, pubkey)
}

// AddRescan queues a rescan for the address. An address queued already
// keeps the lower height.
func (s *Store) AddRescan(r Rescan) error {
	var _, err = s.db.Exec(
		`insert into rescans (address, fromHeight) values (?, ?)
		on conflict (address) do update set fromHeight = min(fromHeight, excluded.fromHeight)`,
		r.Address, r.From,
	)
	return err
}

// Rescans returns the queued rescans.
func (s *Store) Rescans() ([]Rescan, error) {
	var rows, err = s.db.Query(`select address, fromHeight from rescans order by fromHeight, address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rescan
	for rows.Next() {
		var r Rescan
		if err := rows.Scan(&r.Address, &r.From); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRescan removes the rescan once it is done. A rescan queued again
// from a lower height in the meantime stays.
func (s *Store) DeleteRescan(r Rescan) error {
	var _, err = s.db.Exec(`delete from rescans where address = ? and fromHeight >= ?`, r.Address, r.From)
	return err
}

// SavePeer records a known peer address. Idempotent.
func (s *Store) SavePeer(host string, port uint16) error {
	var _, err = s.db.Exec(`insert or ignore into peers (host, port) values (?, ?)`, host, port)
	return err
}

// UpsertPeer stores the advertised services and the last measured latency of
// a connected peer, creating the row when needed.
func (s *Store) UpsertPeer(p Peer) error {
	var _, err = s.db.Exec(
		`insert into peers (host, port, services, latencyMs, okCount, failCount) values (?, ?, ?, ?, 0, 0)
		 on conflict(host, port) do update set services = excluded.services, latencyMs = excluded.latencyMs`,
		p.Host, p.Port, p.Services, p.LatencyMs,
	)
	return err
}

// UpdatePeerServices stores the advertised services of a gossiped peer
// without touching its latency or request counters.
func (s *Store) UpdatePeerServices(host string, port uint16, services uint64) error {
	var _, err = s.db.Exec(
		`insert into peers (host, port, services) values (?, ?, ?)
		 on conflict(host, port) do update set services = excluded.services`,
		host, port, services,
	)
	return err
}

// RecordPeerResult counts one successful or failed request against a peer
// and stores its round-trip latency in milliseconds.
func (s *Store) RecordPeerResult(host string, port uint16, ok bool, latencyMs int64) error {
	var okCount = int64(0)
	var failCount = int64(0)
	if ok { okCount = 1 } else { failCount = 1 }
	var _, err = s.db.Exec(
		`insert into peers (host, port, latencyMs, okCount, failCount) values (?, ?, ?, ?, ?)
		 on conflict(host, port) do update set latencyMs = excluded.latencyMs,
		 okCount = okCount + excluded.okCount, failCount = failCount + excluded.failCount`,
		host, port, latencyMs, okCount, failCount,
	)
	return err
}

// Peers returns every known peer with its statistics.
func (s *Store) Peers() ([]Peer, error) {
	var rows, err = s.db.Query(`select host, port, services, latencyMs, okCount, failCount from peers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		var p Peer
		if err := rows.Scan(&p.Host, &p.Port, &p.Services, &p.LatencyMs, &p.OkCount, &p.FailCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
