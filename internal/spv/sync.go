package spv

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
const (
	headerBatch       = 2000
	filterHeaderBatch = 2000
	filterBatch       = 1000
)

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

// Syncer downloads and validates block headers and BIP158 basic filters and
// matches the filters against the wallet scripts.
type Syncer struct {
	params    *chaincfg.Params
	store     *storage.Store
	chain     *Chain
	scripts   []watchScript
	progress  Progress
	stats     RequestResult
	hdrCh     chan *wire.MsgHeaders
	cfhdrCh   chan *wire.MsgCFHeaders
	fltCh     chan *wire.MsgCFilter
	addrCh    chan *wire.MsgAddr
	pending   map[int32]chainhash.Hash
}

// NewSyncer loads the stored headers into the chain and collects the wallet
// scripts to watch. Stored headers are trusted: they were validated before
// being persisted.
func NewSyncer(params *chaincfg.Params, store *storage.Store, progress Progress) (*Syncer, error) {
	var chain = NewChain(params)
	var count, err = store.HeaderCount()
	if err != nil {
		return nil, fmt.Errorf("load header count: %w", err)
	}
	if count == 0 {
		if err := chain.Add(&params.GenesisBlock.Header); err != nil {
			return nil, fmt.Errorf("add genesis: %w", err)
		}
		var h = params.GenesisBlock.Header
		if err := store.SaveHeader(headerFromWire(&h, 0)); err != nil {
			return nil, fmt.Errorf("store genesis: %w", err)
		}
	} else {
		for height := int32(0); height < count; height++ {
			var h, ok, err = store.HeaderAt(height)
			if err != nil {
				return nil, fmt.Errorf("load header %d: %w", height, err)
			}
			if !ok {
				return nil, fmt.Errorf("stored chain has no header at height %d", height)
			}
			var hdr = wireFromHeader(h)
			if err := chain.appendTrusted(&hdr); err != nil {
				return nil, fmt.Errorf("load header %d: %w", height, err)
			}
		}
	}
	addresses, err := store.Addresses()
	if err != nil {
		return nil, fmt.Errorf("load addresses: %w", err)
	}
	var scripts = make([]watchScript, 0, len(addresses))
	for _, a := range addresses {
		scripts = append(scripts, watchScript{address: a.Address, script: p2wpkhScript(a.Pubkey)})
	}
	return &Syncer{
		params: params, store: store, chain: chain, scripts: scripts, progress: progress,
		hdrCh: make(chan *wire.MsgHeaders, 16), cfhdrCh: make(chan *wire.MsgCFHeaders, 16),
		fltCh: make(chan *wire.MsgCFilter, 16), addrCh: make(chan *wire.MsgAddr, 8),
		pending: make(map[int32]chainhash.Hash),
	}, nil
}

// Listeners returns the peer message listeners wired to this syncer. They
// must be installed before the connection is established. Advertised peer
// addresses are persisted for later syncs.
func (s *Syncer) Listeners() peer.MessageListeners {
	return peer.MessageListeners{
		OnHeaders:   func(_ *peer.Peer, msg *wire.MsgHeaders) { s.hdrCh <- msg },
		OnCFHeaders: func(_ *peer.Peer, msg *wire.MsgCFHeaders) { s.cfhdrCh <- msg },
		OnCFilter:   func(_ *peer.Peer, msg *wire.MsgCFilter) { s.fltCh <- msg },
		OnAddr: func(_ *peer.Peer, msg *wire.MsgAddr) {
			for _, na := range msg.AddrList {
				if na.IP == nil || na.Port == 0 { continue }
				var ip = na.IP.String()
				if na.Services != 0 {
					_ = s.store.UpdatePeerServices(ip, na.Port, uint64(na.Services))
				} else {
					_ = s.store.SavePeer(ip, na.Port)
				}
			}
			select {
			case s.addrCh <- msg:
			default:
			}
		},
	}
}

// Chain exposes the validated header chain.
func (s *Syncer) Chain() *Chain { return s.chain }

// SetStats binds the request result callback to the peer currently in use.
func (s *Syncer) SetStats(stats RequestResult) { s.stats = stats }

// RequestAddresses asks the peer for its known node addresses and waits for
// the first batch. The addresses themselves are persisted by the OnAddr
// listener. Peers may ignore getaddr, so the result is best effort.
func (s *Syncer) RequestAddresses(p *peer.Peer) error {
	s.drainAddr()
	var started = time.Now()
	p.QueueMessage(wire.NewMsgGetAddr(), nil)
	select {
	case <-s.addrCh:
		s.record(true, time.Since(started))
		return nil
	case <-time.After(addrTimeout):
		s.record(false, time.Since(started))
		return fmt.Errorf("no addr response within %s", addrTimeout)
	}
}

func (s *Syncer) drainAddr() {
	for {
		select {
		case <-s.addrCh:
		default:
			return
		}
	}
}

// SyncHeaders requests missing headers from the peer until its tip is
// reached, validating and persisting every batch. It returns the new tip
// height.
func (s *Syncer) SyncHeaders(p *peer.Peer) (int32, error) {
	for {
		var msg = wire.NewMsgGetHeaders()
		msg.ProtocolVersion = p.ProtocolVersion()
		msg.BlockLocatorHashes = s.chain.Locator()
		var started = time.Now()
		p.QueueMessage(msg, nil)
		var batch, err = s.waitHeaders(p)
		s.record(err == nil, time.Since(started))
		if err != nil { return s.chain.Height(), err }
		if len(batch) == 0 { break }
		if err := s.extendHeaders(batch); err != nil {
			return s.chain.Height(), fmt.Errorf("extend headers: %w", err)
		}
		s.report("headers", s.chain.Height())
		if len(batch) < headerBatch { break }
	}
	var tip, _ = s.chain.Tip()
	s.report("headers", tip.Height)
	return tip.Height, nil
}
// SyncFilters downloads and verifies the BIP158 basic filters for every
// header in the chain, matching each filter against the wallet scripts. It
// returns the number of new filters downloaded.
func (s *Syncer) SyncFilters(p *peer.Peer) (int32, error) {
	var start, err = s.store.FilterCount()
	if err != nil { return 0, err }
	var tip, _ = s.chain.Tip()
	var downloaded = int32(0)
	for start <= tip.Height {
		var end = min(start+filterHeaderBatch-1, tip.Height)
		var endHeader, _ = s.chain.HeaderAt(end)
		if err := s.requestFilterHeaders(p, start, endHeader.Hash); err != nil {
			return downloaded, fmt.Errorf("filter headers %d-%d: %w", start, end, err)
		}
		for fStart := start; fStart <= end; fStart += filterBatch {
			var fEnd = min(fStart+filterBatch-1, end)
			var stopHdr, _ = s.chain.HeaderAt(fEnd)
			if err := s.requestFilters(p, fStart, stopHdr.Hash); err != nil {
				return downloaded, fmt.Errorf("filters %d-%d: %w", fStart, fEnd, err)
			}
			downloaded += fEnd - fStart + 1
		}
		start = end + 1
	}
	s.report("filters", tip.Height)
	return downloaded, nil
}

// extendHeaders appends a peer batch to the chain, rewinding a shallow fork
// if the batch does not continue from the tip.
func (s *Syncer) extendHeaders(batch []*wire.BlockHeader) error {
	var first = batch[0].PrevBlock
	var fork = s.chain.HeightOf(first)
	if fork < 0 {
		return fmt.Errorf("peer chain continues from unknown block %s", first)
	}
	if fork < s.chain.Height() {
		s.chain.Truncate(fork)
		if err := s.store.DeleteHeadersFrom(fork); err != nil {
			return fmt.Errorf("rewind stored headers: %w", err)
		}
	}
	for _, hdr := range batch {
		if err := s.chain.Add(hdr); err != nil { return err }
		var height = s.chain.Height()
		if err := s.store.SaveHeader(headerFromWire(hdr, height)); err != nil {
			return fmt.Errorf("store header %d: %w", height, err)
		}
	}
	return nil
}

// requestFilterHeaders fetches and verifies the filter header chain for one
// batch, keeping the headers pending until their filters are downloaded.
func (s *Syncer) requestFilterHeaders(p *peer.Peer, start int32, stop chainhash.Hash) error {
	var msg = wire.NewMsgGetCFHeaders(wire.GCSFilterRegular, uint32(start), &stop)
	var started = time.Now()
	p.QueueMessage(msg, nil)
	var resp, err = s.waitCFHeaders(p)
	s.record(err == nil, time.Since(started))
	if err != nil { return err }
	if resp.FilterType != wire.GCSFilterRegular {
		return fmt.Errorf("unexpected filter type %d", resp.FilterType)
	}
	if resp.StopHash != stop {
		return fmt.Errorf("stop hash %s, want %s", resp.StopHash, stop)
	}
	var wantCount = int(s.chain.HeightOf(stop) - start + 1)
	if len(resp.FilterHashes) != wantCount {
		return fmt.Errorf("got %d filter headers for %d blocks", len(resp.FilterHashes), wantCount)
	}
	var prev chainhash.Hash
	if start == 0 {
		prev = *s.params.GenesisHash
	} else {
		var stored, ok, err = s.store.FilterHeaderAt(start - 1)
		if err != nil { return err }
		if !ok {
			return fmt.Errorf("filter header at height %d not stored", start-1)
		}
		prev = stored
	}
	if resp.PrevFilterHeader != prev {
		return fmt.Errorf("filter header chain break at %d: prev %s, want %s", start, resp.PrevFilterHeader, prev)
	}
	s.pending = make(map[int32]chainhash.Hash, len(resp.FilterHashes))
	for i, hash := range resp.FilterHashes {
		s.pending[start+int32(i)] = *hash
	}
	return nil
}

// requestFilters downloads one batch of filters and verifies each against
// its filter header and the block hash at the same height.
func (s *Syncer) requestFilters(p *peer.Peer, start int32, stop chainhash.Hash) error {
	var msg = wire.NewMsgGetCFilters(wire.GCSFilterRegular, uint32(start), &stop)
	var started = time.Now()
	p.QueueMessage(msg, nil)
	var expected = s.chain.HeightOf(stop) - start + 1
	for range expected {
		var resp, err = s.waitCFilter(p)
		if err != nil {
			s.record(false, time.Since(started))
			return err
		}
		if err := s.storeFilter(resp); err != nil {
			s.record(false, time.Since(started))
			return err
		}
	}
	s.record(true, time.Since(started))
	return nil
}

// storeFilter verifies one downloaded filter and stores it, recording any
// wallet script it matches.
func (s *Syncer) storeFilter(msg *wire.MsgCFilter) error {
	if msg.FilterType != wire.GCSFilterRegular {
		return fmt.Errorf("unexpected filter type %d", msg.FilterType)
	}
	var height = s.chain.HeightOf(msg.BlockHash)
	if height < 0 {
		return fmt.Errorf("cfilter for unknown block %s", msg.BlockHash)
	}
	var want = s.pending[height]
	var got = filterHash(msg.Data)
	if got != want {
		return fmt.Errorf("filter at height %d hashes to %s, want %s", height, got, want)
	}
	var scripts = make([][]byte, len(s.scripts))
	for i, w := range s.scripts {
		scripts[i] = w.script
	}
	var hits, err = matchScripts(msg.Data, &msg.BlockHash, scripts)
	if err != nil {
		return fmt.Errorf("match filter at height %d: %w", height, err)
	}
	if err := s.store.SaveFilter(storage.Filter{Height: height, BlockHash: msg.BlockHash, FilterHeader: got, Data: msg.Data}); err != nil {
		return fmt.Errorf("store filter at height %d: %w", height, err)
	}
	for i, hit := range hits {
		if !hit { continue }
		var w = s.scripts[i]
		if err := s.store.SaveMatch(storage.Match{Height: height, BlockHash: msg.BlockHash, Address: w.address, Script: w.script}); err != nil {
			return fmt.Errorf("store match at height %d: %w", height, err)
		}
	}
	return nil
}

func (s *Syncer) waitHeaders(p *peer.Peer) ([]*wire.BlockHeader, error) {
	var ticker = time.NewTicker(time.Second)
	defer ticker.Stop()
	var deadline = time.Now().Add(requestTimeout)
	for {
		select {
		case msg := <-s.hdrCh:
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

func (s *Syncer) waitCFHeaders(p *peer.Peer) (*wire.MsgCFHeaders, error) {
	var ticker = time.NewTicker(time.Second)
	defer ticker.Stop()
	var deadline = time.Now().Add(requestTimeout)
	for {
		select {
		case msg := <-s.cfhdrCh:
			return msg, nil
		case <-ticker.C:
			if !p.Connected() {
				return nil, fmt.Errorf("peer disconnected")
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("no cfheaders response within %s", requestTimeout)
			}
		}
	}
}

func (s *Syncer) waitCFilter(p *peer.Peer) (*wire.MsgCFilter, error) {
	var ticker = time.NewTicker(time.Second)
	defer ticker.Stop()
	var deadline = time.Now().Add(requestTimeout)
	for {
		select {
		case msg := <-s.fltCh:
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

func (s *Syncer) report(stage string, height int32) {
	if s.progress != nil {
		s.progress(stage, height)
	}
}

func (s *Syncer) record(ok bool, latency time.Duration) {
	if s.stats != nil {
		s.stats(ok, latency)
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
