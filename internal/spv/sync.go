package spv

import "errors"
import "fmt"
import "time"
import "github.com/btcsuite/btcd/btcutil"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/peer"
import "github.com/btcsuite/btcd/wire"
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

// addrTimeout bounds the wait for the first addr batch after getaddr.
const addrTimeout = 5 * time.Second

// Progress reports sync progress: a stage name and the height reached.
type Progress func(stage string, height int32)

// RequestResult reports the outcome of one peer request: ok and the
// round-trip latency.
type RequestResult func(ok bool, latency time.Duration)

// watchScript is one wallet output script kept for filter matching.
type watchScript struct {
	address string
	script  []byte
}

// The sync state is package-scoped: one wallet database, one validated header
// chain and one set of wallet scripts per process.
var params *chaincfg.Params
var store *storage.Store
var chain *Chain
var scripts []watchScript
var progress Progress
var stats RequestResult
var hdrCh chan *wire.MsgHeaders
var cfhdrCh chan *wire.MsgCFHeaders
var fltCh chan *wire.MsgCFilter
var addrCh chan *wire.MsgAddr
var pending map[int32]chainhash.Hash
var seedTime int64
var filterStart int32
var anchorPrev chainhash.Hash
var anchorSet bool

// Init loads the stored headers into the chain and collects the wallet
// scripts to watch. Stored headers are trusted: they were validated before
// being persisted.
func Init(network *chaincfg.Params, db *storage.Store, onProgress Progress) error {
	params = network
	store = db
	progress = onProgress
	chain = NewChain(network)
	hdrCh = make(chan *wire.MsgHeaders, 16)
	cfhdrCh = make(chan *wire.MsgCFHeaders, 16)
	fltCh = make(chan *wire.MsgCFilter, 16)
	addrCh = make(chan *wire.MsgAddr, 8)
	pending = make(map[int32]chainhash.Hash)
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
			if err := chain.appendTrusted(&hdr); err != nil {
				return fmt.Errorf("load header %d: %w", height, err)
			}
		}
	}
	addresses, err := store.Addresses()
	if err != nil {
		return fmt.Errorf("load addresses: %w", err)
	}
	scripts = make([]watchScript, 0, len(addresses))
	for _, a := range addresses {
		scripts = append(scripts, watchScript{address: a.Address, script: p2wpkhScript(a.Pubkey)})
	}
	seedTime = 0
	if meta, err := store.Meta(); err == nil {
		seedTime = meta.CreatedAt
	} else if !errors.Is(err, storage.ErrNoWallet) {
		return fmt.Errorf("load meta: %w", err)
	}
	filterStart = filterStartHeight(chain, seedTime)
	return nil
}

// firstHeaderAtOrAfter returns the height of the first header whose block
// time is at or after the given unix time, or tip+1 when every header is
// older. It is the first block that may contain transactions relevant to a
// wallet created at that time.
func firstHeaderAtOrAfter(c *Chain, unix int64) int32 {
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
func filterStartHeight(c *Chain, unix int64) int32 {
	var first = firstHeaderAtOrAfter(c, unix)
	if first > safetyGap {
		return first - safetyGap
	}
	return 0
}

// Listeners returns the peer message listeners wired to the sync channels.
// They must be installed before the connection is established. Advertised
// peer addresses are persisted for later syncs.
func Listeners() peer.MessageListeners {
	return peer.MessageListeners{
		OnHeaders:   func(_ *peer.Peer, msg *wire.MsgHeaders) { hdrCh <- msg },
		OnCFHeaders: func(_ *peer.Peer, msg *wire.MsgCFHeaders) { cfhdrCh <- msg },
		OnCFilter:   func(_ *peer.Peer, msg *wire.MsgCFilter) { fltCh <- msg },
		OnAddr: func(_ *peer.Peer, msg *wire.MsgAddr) {
			for _, na := range msg.AddrList {
				if na.IP == nil || na.Port == 0 { continue }
				var ip = na.IP.String()
				if na.Services != 0 {
					_ = store.UpdatePeerServices(ip, na.Port, uint64(na.Services))
				} else {
					_ = store.SavePeer(ip, na.Port)
				}
			}
			select {
			case addrCh <- msg:
			default:
			}
		},
	}
}

// HeaderChain exposes the validated header chain.
func HeaderChain() *Chain { return chain }

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

// RequestFilterAnchor asks one peer for the cfheaders batch starting at the
// filter start height and returns the prev_filter_header it reports: the
// candidate anchor for the filter header chain.
func RequestFilterAnchor(p *peer.Peer) (chainhash.Hash, error) {
	var hdr, ok = chain.HeaderAt(filterStart)
	if !ok {
		return chainhash.Hash{}, fmt.Errorf("filter start header %d not in chain", filterStart)
	}
	var resp, err = fetchCFHeaders(p, filterStart, hdr.Hash, anchorRequestTimeout)
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

// SetStats binds the request result callback to the peer currently in use.
func SetStats(onResult RequestResult) { stats = onResult }

// RequestAddresses asks the peer for its known node addresses and waits for
// the first batch. The addresses themselves are persisted by the OnAddr
// listener. Peers may ignore getaddr, so the result is best effort.
func RequestAddresses(p *peer.Peer) error {
	drainAddr()
	var started = time.Now()
	p.QueueMessage(wire.NewMsgGetAddr(), nil)
	select {
	case <-addrCh:
		record(true, time.Since(started))
		return nil
	case <-time.After(addrTimeout):
		record(false, time.Since(started))
		return fmt.Errorf("no addr response within %s", addrTimeout)
	}
}

func drainAddr() {
	for {
		select {
		case <-addrCh:
		default:
			return
		}
	}
}

// SyncHeaders requests missing headers from the peer until its tip is
// reached, validating and persisting every batch. It returns the new tip
// height.
func SyncHeaders(p *peer.Peer) (int32, error) {
	for {
		var msg = wire.NewMsgGetHeaders()
		msg.ProtocolVersion = p.ProtocolVersion()
		msg.BlockLocatorHashes = chain.Locator()
		var started = time.Now()
		p.QueueMessage(msg, nil)
		var batch, err = waitHeaders(p)
		record(err == nil, time.Since(started))
		if err != nil { return chain.Height(), err }
		if len(batch) == 0 { break }
		if err := extendHeaders(batch); err != nil {
			return chain.Height(), fmt.Errorf("extend headers: %w", err)
		}
		report("headers", chain.Height())
		if len(batch) < headerBatch { break }
	}
	var tip, _ = chain.Tip()
	report("headers", tip.Height)
	return tip.Height, nil
}

// SyncFilters downloads and verifies the BIP158 basic filters starting from
// the block at which the wallet seed was created, matching each filter
// against the wallet scripts. The full block header chain is synced first, so
// the filter start height is resolved from the header timestamps. Every
// downloaded filter is pruned right after matching; only its chained header
// is kept to verify the filter header chain. It returns the number of new
// filters downloaded.
func SyncFilters(p *peer.Peer) (int32, error) {
	RefreshFilterStart()
	if err := store.PruneFilterDataFrom(filterStart); err != nil {
		return 0, fmt.Errorf("prune stored filter data: %w", err)
	}
	var start, err = store.FilterResumeHeight(filterStart)
	if err != nil {
		return 0, fmt.Errorf("filter resume height: %w", err)
	}
	var tip, _ = chain.Tip()
	var downloaded = int32(0)
	report("filters start", start)
	for start <= tip.Height {
		var end = min(start+filterHeaderBatch-1, tip.Height)
		var endHeader, _ = chain.HeaderAt(end)
		if err := requestFilterHeaders(p, start, endHeader.Hash); err != nil {
			return downloaded, fmt.Errorf("filter headers %d-%d: %w", start, end, err)
		}
		for fStart := start; fStart <= end; fStart += filterBatch {
			var fEnd = min(fStart+filterBatch-1, end)
			var stopHdr, _ = chain.HeaderAt(fEnd)
			if err := requestFilters(p, fStart, stopHdr.Hash); err != nil {
				return downloaded, fmt.Errorf("filters %d-%d: %w", fStart, fEnd, err)
			}
			downloaded += fEnd - fStart + 1
		}
		start = end + 1
	}
	report("filters", tip.Height)
	return downloaded, nil
}

// extendHeaders appends a peer batch to the chain, rewinding a shallow fork
// if the batch does not continue from the tip.
func extendHeaders(batch []*wire.BlockHeader) error {
	var first = batch[0].PrevBlock
	var fork = chain.HeightOf(first)
	if fork < 0 {
		return fmt.Errorf("peer chain continues from unknown block %s", first)
	}
	if fork < chain.Height() {
		chain.Truncate(fork)
		if err := store.DeleteHeadersFrom(fork); err != nil {
			return fmt.Errorf("rewind stored headers: %w", err)
		}
		if err := store.DeleteFiltersFrom(fork); err != nil {
			return fmt.Errorf("rewind stored filters: %w", err)
		}
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

// requestFilterHeaders fetches and verifies the filter header chain for one
// batch, keeping the headers pending until their filters are downloaded.
func requestFilterHeaders(p *peer.Peer, start int32, stop chainhash.Hash) error {
	var started = time.Now()
	var resp, err = fetchCFHeaders(p, start, stop, requestTimeout)
	record(err == nil, time.Since(started))
	if err != nil { return err }
	if err := checkFilterPrev(start, resp.PrevFilterHeader); err != nil {
		return err
	}
	pending = make(map[int32]chainhash.Hash, len(resp.FilterHashes))
	for i, hash := range resp.FilterHashes {
		pending[start+int32(i)] = *hash
	}
	return nil
}

// fetchCFHeaders sends one getcfheaders request and validates the response
// type, stop hash and filter header count. Stale responses from peers that
// timed out earlier are discarded first.
func fetchCFHeaders(p *peer.Peer, start int32, stop chainhash.Hash, timeout time.Duration) (*wire.MsgCFHeaders, error) {
	drainCFHeaders()
	var msg = wire.NewMsgGetCFHeaders(wire.GCSFilterRegular, uint32(start), &stop)
	p.QueueMessage(msg, nil)
	var resp, err = waitCFHeadersFor(p, timeout)
	if err != nil { return nil, err }
	if resp.FilterType != wire.GCSFilterRegular {
		return nil, fmt.Errorf("unexpected filter type %d", resp.FilterType)
	}
	if resp.StopHash != stop {
		return nil, fmt.Errorf("stop hash %s, want %s", resp.StopHash, stop)
	}
	var wantCount = int(chain.HeightOf(stop) - start + 1)
	if len(resp.FilterHashes) != wantCount {
		return nil, fmt.Errorf("got %d filter headers for %d blocks", len(resp.FilterHashes), wantCount)
	}
	return resp, nil
}

// drainCFHeaders discards any stale cfheaders responses left by peers that
// timed out earlier, so they are not mistaken for the next request's answer.
func drainCFHeaders() {
	for {
		select {
		case <-cfhdrCh:
		default:
			return
		}
	}
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

// requestFilters downloads one batch of filters and verifies each against
// its filter header and the block hash at the same height.
func requestFilters(p *peer.Peer, start int32, stop chainhash.Hash) error {
	var msg = wire.NewMsgGetCFilters(wire.GCSFilterRegular, uint32(start), &stop)
	var started = time.Now()
	p.QueueMessage(msg, nil)
	var expected = chain.HeightOf(stop) - start + 1
	for range expected {
		var resp, err = waitCFilter(p)
		if err != nil {
			record(false, time.Since(started))
			return err
		}
		if err := storeFilter(resp); err != nil {
			record(false, time.Since(started))
			return err
		}
	}
	record(true, time.Since(started))
	return nil
}

// storeFilter verifies one downloaded filter against the cfheaders hash and
// stores it with its chained filter header, recording any wallet script it
// matches. The filter data is pruned immediately after the row is stored, so
// only the chained header remains to verify the filter header chain.
func storeFilter(msg *wire.MsgCFilter) error {
	if msg.FilterType != wire.GCSFilterRegular {
		return fmt.Errorf("unexpected filter type %d", msg.FilterType)
	}
	var height = chain.HeightOf(msg.BlockHash)
	if height < 0 {
		return fmt.Errorf("cfilter for unknown block %s", msg.BlockHash)
	}
	var raw = filterHash(msg.Data)
	if raw != pending[height] {
		return fmt.Errorf("filter at height %d hashes to %s, want %s", height, raw, pending[height])
	}
	var prev = chainhash.Hash{}
	if height > 0 {
		if height == filterStart {
			prev = anchorPrev
		} else {
			var stored, ok, err = store.FilterHeaderAt(height - 1)
			if err != nil { return err }
			if !ok {
				return fmt.Errorf("filter header at height %d not stored", height-1)
			}
			prev = stored
		}
	}
	var header = filterHeader(raw, prev)
	var scriptBytes = make([][]byte, len(scripts))
	for i, w := range scripts {
		scriptBytes[i] = w.script
	}
	var hits, err = matchScripts(msg.Data, &msg.BlockHash, scriptBytes)
	if err != nil {
		return fmt.Errorf("match filter at height %d: %w", height, err)
	}
	for i, hit := range hits {
		if !hit { continue }
		var w = scripts[i]
		if err := store.SaveMatch(storage.Match{Height: height, BlockHash: msg.BlockHash, Address: w.address, Script: w.script}); err != nil {
			return fmt.Errorf("store match at height %d: %w", height, err)
		}
	}
	if err := store.SaveFilter(storage.Filter{Height: height, BlockHash: msg.BlockHash, FilterHeader: header, Data: msg.Data}); err != nil {
		return fmt.Errorf("store filter at height %d: %w", height, err)
	}
	if err := store.PruneFilterData(height); err != nil {
		return fmt.Errorf("prune filter at height %d: %w", height, err)
	}
	return nil
}

func waitHeaders(p *peer.Peer) ([]*wire.BlockHeader, error) {
	var ticker = time.NewTicker(time.Second)
	defer ticker.Stop()
	var deadline = time.Now().Add(requestTimeout)
	for {
		select {
		case msg := <-hdrCh:
			return msg.Headers, nil
		case <-ticker.C:
			if !p.Connected() {
				return nil, fmt.Errorf("peer disconnected")
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("no headers response within %s", requestTimeout)
			}
		}
	}
}

func waitCFHeaders(p *peer.Peer) (*wire.MsgCFHeaders, error) {
	return waitCFHeadersFor(p, requestTimeout)
}

func waitCFHeadersFor(p *peer.Peer, timeout time.Duration) (*wire.MsgCFHeaders, error) {
	var ticker = time.NewTicker(time.Second)
	defer ticker.Stop()
	var deadline = time.Now().Add(timeout)
	for {
		select {
		case msg := <-cfhdrCh:
			return msg, nil
		case <-ticker.C:
			if !p.Connected() {
				return nil, fmt.Errorf("peer disconnected")
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("no cfheaders response within %s", timeout)
			}
		}
	}
}

func waitCFilter(p *peer.Peer) (*wire.MsgCFilter, error) {
	var ticker = time.NewTicker(time.Second)
	defer ticker.Stop()
	var deadline = time.Now().Add(requestTimeout)
	for {
		select {
		case msg := <-fltCh:
			return msg, nil
		case <-ticker.C:
			if !p.Connected() {
				return nil, fmt.Errorf("peer disconnected")
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("no cfilter response within %s", requestTimeout)
			}
		}
	}
}

func report(stage string, height int32) {
	if progress != nil {
		progress(stage, height)
	}
}

func record(ok bool, latency time.Duration) {
	if stats != nil {
		stats(ok, latency)
	}
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
