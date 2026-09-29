// Package sync keeps the wallet synchronised with the Bitcoin network: it holds
// a small pool of P2P peers, downloads and validates block headers and BIP158
// filters, matches wallet scripts and follows new blocks while the pool stays
// connected.
package sync

import "errors"
import "fmt"
import "log"
import "time"
import "github.com/btcsuite/btcd/btcutil"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/spv"
import "bitfyn/internal/storage"

// headerBatch and filterHeaderBatch bound a single request per the protocol
// limits; filterBatch is the smaller limit for full filters.
const headerBatch = 2000
const filterHeaderBatch = 2000
const filterBatch = 1000

// safetyGap starts the filter download a few blocks before the wallet seed
// block so no relevant transaction is missed by timestamp jitter.
const safetyGap = int32(10)

// anchorRequestTimeout bounds one peer's answer during the filter header
// anchor majority proof. Ten peers are polled in turn, so it is shorter than
// a normal request.
const anchorRequestTimeout = 20 * time.Second

// requestTimeout bounds how long a single request may wait for a response.
const requestTimeout = 90 * time.Second

// watchScript is one wallet output script kept for filter matching.
type watchScript struct {
	address string
	script  []byte
}

// The sync state is package-scoped: one wallet database, one validated header
// chain and one set of wallet scripts per process.
var params *chaincfg.Params
var store *storage.Store
var chain *spv.Chain
var scripts []watchScript
var scriptIndex map[string]int
var outpoints map[wire.OutPoint]string
var seedTime int64
var filterStart int32
var anchorPrev chainhash.Hash
var anchorSet bool

// Init loads the stored headers into the chain and collects the wallet
// scripts and coins to watch. Stored headers are trusted: they were validated
// before being persisted.
func Init(network *chaincfg.Params, db *storage.Store) error {
	params = network
	store = db
	chain = spv.NewChain(network)
	anchorPrev = chainhash.Hash{}
	anchorSet = false
	var count, err = store.HeaderCount()
	if err != nil {
		return fmt.Errorf("load header count: %w", err)
	}
	if count == 0 {
		if err := chain.Add(&params.GenesisBlock.Header); err != nil {
			return fmt.Errorf("add genesis: %w", err)
		}
		var h = params.GenesisBlock.Header
		if err := store.SaveHeader(headerFromWire(&h, 0)); err != nil {
			return fmt.Errorf("store genesis: %w", err)
		}
	} else {
		for height := int32(0); height < count; height++ {
			var h, ok, err = store.HeaderAt(height)
			if err != nil {
				return fmt.Errorf("load header %d: %w", height, err)
			}
			if !ok {
				return fmt.Errorf("stored chain has no header at height %d", height)
			}
			var hdr = wireFromHeader(h)
			if err := chain.AppendTrusted(&hdr); err != nil {
				return fmt.Errorf("load header %d: %w", height, err)
			}
		}
	}
	addresses, err := store.Addresses()
	if err != nil {
		return fmt.Errorf("load addresses: %w", err)
	}
	scripts = make([]watchScript, 0, len(addresses))
	scriptIndex = make(map[string]int, len(addresses))
	for _, a := range addresses {
		var script = p2wpkhScript(a.Pubkey)
		scriptIndex[string(script)] = len(scripts)
		scripts = append(scripts, watchScript{address: a.Address, script: script})
	}
	if err := loadOutpoints(); err != nil {
		return err
	}
	seedTime = 0
	if meta, err := store.Meta(); err == nil {
		seedTime = meta.CreatedAt
	} else if !errors.Is(err, storage.ErrNoWallet) {
		return fmt.Errorf("load meta: %w", err)
	}
	filterStart = filterStartHeight(chain, seedTime)
	log.Printf("sync: loaded %d headers, tip %d, watching %d addresses and %d coins",
		chain.Height()+1, chain.Height(), len(scripts), len(outpoints))
	return nil
}

// firstHeaderAtOrAfter returns the height of the first header whose block
// time is at or after the given unix time, or tip+1 when every header is
// older. It is the first block that may contain transactions relevant to a
// wallet created at that time.
func firstHeaderAtOrAfter(c *spv.Chain, unix int64) int32 {
	if unix <= 0 {
		return 0
	}
	for height := int32(0); height <= c.Height(); height++ {
		var hdr, ok = c.HeaderAt(height)
		if ok && hdr.Timestamp.Unix() >= unix {
			return height
		}
	}
	return c.Height() + 1
}

// filterStartHeight returns the height at which filter download begins: a
// safety gap of safetyGap blocks before the first block at or after the
// wallet seed time, clamped to genesis.
func filterStartHeight(c *spv.Chain, unix int64) int32 {
	var first = firstHeaderAtOrAfter(c, unix)
	if first > safetyGap {
		return first - safetyGap
	}
	return 0
}

// HeaderChain exposes the validated header chain.
func HeaderChain() *spv.Chain { return chain }

// FilterStart returns the height at which filter download begins: safetyGap
// blocks before the first block whose time is at or after the wallet seed
// creation time.
func FilterStart() int32 { return filterStart }

// RefreshFilterStart recomputes the filter start height from the current
// header chain. It must be called after the header sync: the chain may have
// grown past the wallet seed time, which moves the start height.
func RefreshFilterStart() {
	filterStart = filterStartHeight(chain, seedTime)
}

// NeedsAnchor reports whether the first filter header must be majority
// proven before the filter download starts.
func NeedsAnchor() bool {
	return filterStart > 0 && filterStart <= chain.Height()
}

// SetFilterAnchor stores the majority-proven anchor of the filter header
// chain. The first cfheaders batch must then match it.
func SetFilterAnchor(anchor chainhash.Hash) {
	anchorPrev = anchor
	anchorSet = true
}

// requestFilterAnchor asks one peer for the cfheaders batch starting at the
// filter start height and returns the prev_filter_header it reports: the
// candidate anchor for the filter header chain.
func requestFilterAnchor(c *conn) (chainhash.Hash, error) {
	var hdr, ok = chain.HeaderAt(filterStart)
	if !ok {
		return chainhash.Hash{}, fmt.Errorf("filter start header %d not in chain", filterStart)
	}
	var resp, err = c.getCFHeaders(filterStart, hdr.Hash, anchorRequestTimeout)
	if err != nil {
		return chainhash.Hash{}, err
	}
	return resp.PrevFilterHeader, nil
}

// MajorityAnchor returns the anchor reported by a strict majority of the
// peer votes. Empty input and ties without a majority are errors.
func MajorityAnchor(votes []chainhash.Hash) (chainhash.Hash, error) {
	if len(votes) == 0 {
		return chainhash.Hash{}, fmt.Errorf("no anchor votes")
	}
	var counts = make(map[chainhash.Hash]int, len(votes))
	for _, vote := range votes {
		counts[vote]++
	}
	var best = votes[0]
	for vote, n := range counts {
		if n > counts[best] {
			best = vote
		}
	}
	if counts[best]*2 <= len(votes) {
		return chainhash.Hash{}, fmt.Errorf("no majority anchor: %d of %d peers agree", counts[best], len(votes))
	}
	return best, nil
}

// syncHeaders requests missing headers from the peer until its tip is
// reached, validating and persisting every batch. It returns the new tip
// height.
func syncHeaders(c *conn) (int32, error) {
	for {
		var batch, err = c.getHeaders(chain.Locator())
		if err != nil { return chain.Height(), err }
		if len(batch) == 0 { break }
		var from = chain.Height() + 1
		if err := extendHeaders(batch); err != nil {
			return chain.Height(), fmt.Errorf("extend headers: %w", err)
		}
		log.Printf("sync: headers %d-%d from %s", min(from, chain.Height()), chain.Height(), c.addr)
		report(StateHeaders, chain.Height())
		if len(batch) < headerBatch { break }
	}
	return chain.Height(), nil
}

// extendHeaders appends a peer batch to the chain, rewinding a shallow fork
// if the batch does not continue from the tip. Filters, matches and wallet
// transactions above the fork point are dropped with the rewound headers.
func extendHeaders(batch []*wire.BlockHeader) error {
	var first = batch[0].PrevBlock
	var fork = chain.HeightOf(first)
	if fork < 0 {
		return fmt.Errorf("peer chain continues from unknown block %s", first)
	}
	if fork < chain.Height() {
		log.Printf("sync: reorg, rewinding from height %d to %d", chain.Height(), fork)
		if err := rewindTo(fork); err != nil { return err }
	}
	for _, hdr := range batch {
		if err := chain.Add(hdr); err != nil { return err }
		var height = chain.Height()
		if err := store.SaveHeader(headerFromWire(hdr, height)); err != nil {
			return fmt.Errorf("store header %d: %w", height, err)
		}
	}
	return nil
}

// rewindTo drops every header and every block-derived row above the height.
func rewindTo(fork int32) error {
	chain.Truncate(fork)
	if err := store.DeleteHeadersFrom(fork); err != nil {
		return fmt.Errorf("rewind stored headers: %w", err)
	}
	if err := store.DeleteFiltersFrom(fork); err != nil {
		return fmt.Errorf("rewind stored filters: %w", err)
	}
	if err := store.DeleteMatchesFrom(fork); err != nil {
		return fmt.Errorf("rewind stored matches: %w", err)
	}
	if err := store.DeleteTransactionsFrom(fork); err != nil {
		return fmt.Errorf("rewind stored transactions: %w", err)
	}
	return loadOutpoints()
}

// checkFilterPrev verifies the prev_filter_header of a cfheaders batch
// against the stored chain of chained filter headers. Bitcoin Core and btcd
// send the null hash for the first batch at height 0; the genesis hash is
// also accepted there for compatibility with stricter BIP157 readings. The
// first batch at the wallet seed height has no stored predecessor; it must
// match the majority-proven anchor when one is set, and otherwise the peer's
// prev_filter_header is trusted as the anchor of the filter header chain.
func checkFilterPrev(start int32, got chainhash.Hash) error {
	if start == filterStart && filterStart > 0 {
		if anchorSet {
			if got != anchorPrev {
				return fmt.Errorf("filter header anchor %s, want majority-proven %s", got, anchorPrev)
			}
			return nil
		}
		anchorPrev = got
		return nil
	}
	var want = chainhash.Hash{}
	if start > 0 {
		var stored, ok, err = store.FilterHeaderAt(start - 1)
		if err != nil { return err }
		if !ok {
			return fmt.Errorf("filter header at height %d not stored", start-1)
		}
		want = stored
	}
	if got == want {
		return nil
	}
	if start == 0 && got == *params.GenesisHash {
		return nil
	}
	return fmt.Errorf("filter header chain break at %d: prev %s, want %s", start, got, want)
}

// prevFilterHeader returns the chained filter header the filter at the
// height links to: the anchor at the wallet seed height, the null hash at
// genesis and the stored header of the previous block otherwise.
func prevFilterHeader(height int32) (chainhash.Hash, error) {
	if height == 0 {
		return chainhash.Hash{}, nil
	}
	if height == filterStart {
		return anchorPrev, nil
	}
	var stored, ok, err = store.FilterHeaderAt(height - 1)
	if err != nil { return chainhash.Hash{}, err }
	if !ok {
		return chainhash.Hash{}, fmt.Errorf("filter header at height %d not stored", height-1)
	}
	return stored, nil
}

// p2wpkhScript is the P2WPKH output script of a compressed public key.
func p2wpkhScript(pubkey []byte) []byte {
	var hash = btcutil.Hash160(pubkey)
	var script = make([]byte, 0, 22)
	script = append(script, 0x00, 0x14)
	script = append(script, hash...)
	return script
}

// headerFromWire converts a wire header into the stored form.
func headerFromWire(hdr *wire.BlockHeader, height int32) storage.Header {
	return storage.Header{
		Height: height, Hash: hdr.BlockHash(), PrevHash: hdr.PrevBlock, MerkleRoot: hdr.MerkleRoot,
		Version: hdr.Version, Timestamp: hdr.Timestamp.Unix(), Bits: hdr.Bits, Nonce: hdr.Nonce,
	}
}

// wireFromHeader converts a stored header back into the wire form.
func wireFromHeader(h storage.Header) wire.BlockHeader {
	return wire.BlockHeader{
		Version: h.Version, PrevBlock: h.PrevHash, MerkleRoot: h.MerkleRoot,
		Timestamp: time.Unix(h.Timestamp, 0), Bits: h.Bits, Nonce: h.Nonce,
	}
}
