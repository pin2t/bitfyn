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

// StoredAddress is one derived address row with its public key.
type StoredAddress struct {
	Index   uint32
	Address string
	Pubkey  []byte
}

// Peer is one known network peer address.
type Peer struct {
	Host string
	Port uint16
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

// SaveFilter persists one verified BIP158 basic filter.
func (s *Store) SaveFilter(f Filter) error {
	var _, err = s.db.Exec(
		`insert or replace into cfilters (height, blockHash, filterHeader, filterData) values (?, ?, ?, ?)`,
		f.Height, f.BlockHash[:], f.FilterHeader[:], f.Data,
	)
	return err
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

// Addresses returns all derived addresses with their public keys.
func (s *Store) Addresses() ([]StoredAddress, error) {
	var rows, err = s.db.Query(`select idx, address, pubkey from addresses order by idx`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredAddress
	for rows.Next() {
		var a StoredAddress
		if err := rows.Scan(&a.Index, &a.Address, &a.Pubkey); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SavePeer records a known peer address. Idempotent.
func (s *Store) SavePeer(host string, port uint16) error {
	var _, err = s.db.Exec(`insert or ignore into peers (host, port) values (?, ?)`, host, port)
	return err
}

// Peers returns every known peer address.
func (s *Store) Peers() ([]Peer, error) {
	var rows, err = s.db.Query(`select host, port from peers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		var p Peer
		if err := rows.Scan(&p.Host, &p.Port); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
