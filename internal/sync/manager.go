package sync

import "context"
import "fmt"
import "log"
import "math/rand/v2"
import "net"
import "slices"
import "strconv"
import "sync"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/p2p"
import "bitfyn/internal/storage"

// TargetPeers is the number of peer connections the manager keeps open.
const TargetPeers = 3

// connectWorkers is how many candidates are dialed at once while the pool is
// first filled.
const connectWorkers = 5

// anchorPeerLimit is how many peers are asked for the first filter header
// when majority proving the filter header chain anchor; anchorDialers is how
// many of them are dialed at once.
const anchorPeerLimit = 10
const anchorDialers = 8

// pinnedMaxBackoff caps the delay between reconnect attempts to the pinned
// peer; a connection to it lasting pinnedStableAfter resets the backoff, a
// shorter one counts as a failed attempt.
const pinnedMaxBackoff = time.Minute
const pinnedStableAfter = 30 * time.Second

// pinnedFallbackAfter is how long the pinned peer may be away before the
// sync falls back to the other peers until it is back.
const pinnedFallbackAfter = 5 * time.Minute

// retryDelay paces reconnect attempts and failed sync rounds.
const retryDelay = 5 * time.Second

// pollInterval is the fallback re-sync period: new blocks normally wake the
// sync through peer announcements, but a missed announcement must not stall
// the wallet.
const pollInterval = 10 * time.Minute

// State is what the sync is currently doing.
type State int

const StateIdle State = 0
const StateHeaders State = 1
const StateFilters State = 2

// Status is a snapshot of the sync for display: the number of connected
// peers, the current activity, the block height that activity has reached,
// whether the wallet is synced to the tip of the chain, and the wallet balance
// in satoshis: confirmed, and the change pending unconfirmed transactions make
// to it.
type Status struct {
	State   State
	Peers   int
	Height  int32
	Synced  bool
	Balance int64
	Pending int64
}

// Bars returns the connectivity level from 0 to TargetPeers: one per
// connected peer.
func (s Status) Bars() int {
	return min(s.Peers, TargetPeers)
}

// String is the status line: not connected, connected once synced to the
// tip, or syncing with the block number reached so far.
func (s Status) String() string {
	if s.Peers == 0 {
		return "Not connected"
	}
	if !s.Synced {
		return fmt.Sprintf("Syncing (%d)...", s.Height)
	}
	return "Connected"
}

var mu sync.Mutex
var emitMu sync.Mutex
var wg sync.WaitGroup
var pool []*conn
var activity State
var height int32
var notify func(Status)
var stopped = true
var stop chan struct{}
var dialCtx = context.Background()
var cancelDials context.CancelFunc = func() {}
var wake chan struct{}
var poolChanged chan struct{}
var pinnedAddr string
var pinnedSeen time.Time
var cancelPinned context.CancelFunc = func() {}
var roundOK bool
var announced int
var syncedThrough int
var candidates []string
var candNext int
var anchorTried bool

// Start opens the wallet database state and begins syncing in the
// background: it keeps TargetPeers peers connected, syncs headers and filters
// and then stays connected to follow new blocks. Every change of the
// connection count or sync activity is reported through onStatus, from any
// goroutine. pinned is an optional host:port peer that is assumed to be
// always available: it is never dropped or replaced, it is reconnected
// whenever the connection is lost, and headers and filters are always synced
// from it while it is connected. The other peers cross-check its filters, and
// take over the sync once it has been away for pinnedFallbackAfter.
func Start(network *chaincfg.Params, db *storage.Store, pinned string, onStatus func(Status)) error {
	if pinned != "" {
		if _, _, err := net.SplitHostPort(pinned); err != nil {
			return fmt.Errorf("peer address %q must include a port", pinned)
		}
	}
	if err := Init(network, db); err != nil { return err }
	mu.Lock()
	pool = nil
	activity = StateIdle
	height = chain.Height()
	notify = onStatus
	stopped = false
	stop = make(chan struct{})
	dialCtx, cancelDials = context.WithCancel(context.Background())
	wake = make(chan struct{}, 1)
	poolChanged = make(chan struct{}, 1)
	pinnedAddr = pinned
	pinnedSeen = time.Now()
	cancelPinned = func() {}
	var pinnedCtx context.Context
	if pinned != "" { pinnedCtx, cancelPinned = context.WithCancel(dialCtx) }
	roundOK = false
	announced = 0
	syncedThrough = 0
	candidates = nil
	candNext = 0
	anchorTried = false
	wg.Add(2)
	if pinned != "" { wg.Add(1) }
	mu.Unlock()
	emit()
	go maintain()
	go syncLoop()
	if pinned != "" { go keepPinned(pinnedCtx, pinned) }
	return nil
}

// SetPinned pins the running sync to another peer, as Start's pinned does,
// or unpins it with "". The connection to the peer pinned before is closed,
// and so is a pool connection to the new peer, which is dialed again as the
// pinned peer. Headers and filters are synced from it once it connects. It
// does nothing while the sync is stopped.
func SetPinned(addr string) error {
	if addr != "" {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("peer address %q must include a port", addr)
		}
	}
	mu.Lock()
	if stopped || addr == pinnedAddr {
		mu.Unlock()
		return nil
	}
	var old = pinnedAddr
	cancelPinned()
	cancelPinned = func() {}
	pinnedAddr = addr
	pinnedSeen = time.Now()
	var closing []*conn
	for _, c := range pool {
		if c.pinned || c.addr == addr { closing = append(closing, c) }
	}
	var ctx context.Context
	if addr != "" {
		ctx, cancelPinned = context.WithCancel(dialCtx)
		wg.Add(1)
	}
	mu.Unlock()
	switch {
	case addr == "":
		log.Printf("sync: pinned peer %s removed", old)
	case old == "":
		log.Printf("sync: pinned peer set to %s", addr)
	default:
		log.Printf("sync: pinned peer changed from %s to %s", old, addr)
	}
	for _, c := range closing {
		c.peer.Disconnect()
	}
	if addr != "" { go keepPinned(ctx, addr) }
	notifyPool()
	wakeSync()
	emit()
	return nil
}

// currentPinned returns the pinned peer, "" when none is set.
func currentPinned() string {
	mu.Lock()
	defer mu.Unlock()
	return pinnedAddr
}

// Stop ends the background sync, closes every peer connection and waits for
// the sync goroutines to finish. It is safe to call more than once.
func Stop() {
	mu.Lock()
	if stopped {
		mu.Unlock()
		return
	}
	stopped = true
	close(stop)
	cancelDials()
	var open = append([]*conn(nil), pool...)
	mu.Unlock()
	for _, c := range open {
		c.peer.Disconnect()
	}
	wg.Wait()
}

// emit reports the current status. Callbacks are serialised so they arrive in
// the order the status changed.
func emit() {
	emitMu.Lock()
	defer emitMu.Unlock()
	var confirmed, pending = walletBalance()
	mu.Lock()
	var status = Status{State: activity, Peers: len(pool), Height: height, Synced: syncedLocked(), Balance: confirmed, Pending: pending}
	var cb = notify
	var live = !stopped
	mu.Unlock()
	if cb != nil && live {
		cb(status)
	}
}

// syncedLocked, with mu held, says whether the wallet is synced to the tip:
// idle after a successful round, no unknown block announced since that round
// started, and no connected peer advertising a higher tip.
func syncedLocked() bool {
	if activity != StateIdle || !roundOK || announced != syncedThrough { return false }
	for _, c := range pool {
		if c.peer.LastBlock() > height { return false }
	}
	return true
}

// blockAnnounced notes that a peer announced a block not in the chain, so the
// wallet is behind until the next successful round.
func blockAnnounced() {
	mu.Lock()
	announced++
	mu.Unlock()
	emit()
}

// report records the sync activity and the height it reached.
func report(state State, h int32) {
	mu.Lock()
	activity = state
	height = h
	mu.Unlock()
	emit()
}

// wakeSync asks the sync loop to run another round.
func wakeSync() {
	select {
	case wake <- struct{}{}:
	default:
	}
}

func notifyPool() {
	select {
	case poolChanged <- struct{}{}:
	default:
	}
}

// pause waits for the delay and reports whether the manager is still running.
func pause(delay time.Duration) bool {
	select {
	case <-stop:
		return false
	case <-time.After(delay):
		return true
	}
}

// maintain fills the pool with fillPool and then keeps it filled up to
// TargetPeers, dialing as many next candidates concurrently as peers are
// missing. While a pinned peer is set, one slot is kept for it; keepPinned
// dials it on its own.
func maintain() {
	defer wg.Done()
	fillPool()
	for {
		select {
		case <-stop:
			return
		default:
		}
		var need = openSlots()
		var wait = retryDelay
		if need > 0 {
			var addrs = takeCandidates(need)
			var dials sync.WaitGroup
			for _, addr := range addrs {
				dials.Add(1)
				go func() {
					defer dials.Done()
					connect(dialCtx, addr, false)
				}()
			}
			dials.Wait()
			if len(addrs) > 0 { wait = time.Second }
		}
		select {
		case <-stop:
			return
		case <-poolChanged:
		case <-time.After(wait):
		}
	}
}

// fillPool connects the first peers quickly: connectWorkers goroutines each
// take the next candidate of a freshly shuffled list, dial it, and repeat
// until the pool is full or the list runs out. The handshakes still in flight
// once the pool is full are abandoned. The candidates not dialed are left for
// maintain to continue with.
func fillPool() {
	if openSlots() <= 0 { return }
	var list, err = peerCandidates(params, store, currentPinned())
	if err != nil {
		log.Printf("peer discovery: %v", err)
		return
	}
	var ctx, cancel = context.WithCancel(dialCtx)
	defer cancel()
	var work = make(chan string)
	var workers sync.WaitGroup
	for range connectWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for addr := range work {
				if connect(ctx, addr, false) && openSlots() <= 0 { cancel() }
			}
		}()
	}
	var fed = 0
feed:
	for fed < len(list) && openSlots() > 0 {
		select {
		case work <- list[fed]:
			fed++
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	workers.Wait()
	mu.Lock()
	candidates = list
	candNext = fed
	var count = len(pool)
	mu.Unlock()
	log.Printf("peers: initial connect dialed %d of %d candidates, %d connected", fed, len(list), count)
}

// openSlots is how many more peers the pool takes: TargetPeers less the
// connected peers, and less one kept for a pinned peer while it is away.
func openSlots() int {
	mu.Lock()
	defer mu.Unlock()
	return openSlotsLocked()
}

// openSlotsLocked is openSlots with mu held.
func openSlotsLocked() int {
	var n = TargetPeers - len(pool)
	if pinnedAddr != "" && !slices.ContainsFunc(pool, func(c *conn) bool { return c.pinned }) { n-- }
	return n
}

// keepPinned keeps the pinned peer connected until ctx is cancelled, as
// the sync stops or another peer is pinned: it dials it, waits for the
// connection to drop and dials again. It backs off while the peer is
// unreachable or keeps dropping the connection soon after the handshake, as
// a node with full inbound slots does when it evicts its newest peer.
func keepPinned(ctx context.Context, addr string) {
	defer wg.Done()
	var backoff = retryDelay
	for {
		if connect(ctx, addr, true) {
			var c = pinnedConn()
			var since = time.Now()
			if c != nil {
				select {
				case <-ctx.Done():
					return
				case <-c.quit:
				}
			}
			var lasted = time.Since(since)
			if lasted >= pinnedStableAfter {
				backoff = retryDelay
				continue
			}
			log.Printf("pinned peer %s dropped the connection after %s", addr, lasted.Round(time.Millisecond))
		}
		if ctx.Err() != nil { return }
		log.Printf("pinned peer %s: retrying in %s", addr, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, pinnedMaxBackoff)
	}
}

func poolSize() int {
	mu.Lock()
	defer mu.Unlock()
	return len(pool)
}

// poolPeers returns a snapshot of the connected peers, oldest first.
func poolPeers() []*conn {
	mu.Lock()
	defer mu.Unlock()
	return append([]*conn(nil), pool...)
}

// livePeers returns the peers of the list that are still connected.
func livePeers(peers []*conn) []*conn {
	var out []*conn
	for _, c := range peers {
		if c.live() { out = append(out, c) }
	}
	return out
}

// pinnedConn returns the connected pinned peer, if any.
func pinnedConn() *conn {
	for _, c := range livePeers(poolPeers()) {
		if c.pinned { return c }
	}
	return nil
}

// orderedPeers returns the connected peers with the primary first: the
// pinned peer when one is set, the oldest connection otherwise.
func orderedPeers() []*conn {
	var peers = livePeers(poolPeers())
	var out = make([]*conn, 0, len(peers))
	for _, c := range peers {
		if c.pinned { out = append(out, c) }
	}
	for _, c := range peers {
		if !c.pinned { out = append(out, c) }
	}
	return out
}

// primary returns the connected peer that drives the sync. With a pinned
// peer set, only the pinned peer qualifies, nil meaning waiting for it, until
// it has been away for pinnedFallbackAfter: then the oldest other peer does.
func primary() *conn {
	if currentPinned() != "" {
		if c := pinnedConn(); c != nil { return c }
		if !pinnedAway() { return nil }
	}
	var peers = orderedPeers()
	if len(peers) == 0 { return nil }
	return peers[0]
}

// filterPeers returns up to TargetPeers connected peers to download and
// cross-check filters from, the primary first. It is empty while a pinned
// peer is set but not connected.
func filterPeers() []*conn {
	if primary() == nil { return nil }
	var peers = orderedPeers()
	return peers[:min(len(peers), TargetPeers)]
}

// pinnedAway says whether a pinned peer is set but has not been connected
// for pinnedFallbackAfter, so the sync falls back to the other peers.
func pinnedAway() bool {
	mu.Lock()
	var addr, seen = pinnedAddr, pinnedSeen
	mu.Unlock()
	if addr == "" || pinnedConn() != nil { return false }
	return time.Since(seen) >= pinnedFallbackAfter
}

// fallbackTimer fires when the sync falls back from the away pinned peer to
// the others. It is nil, never firing, without a pinned peer or once the
// fallback is due.
func fallbackTimer() <-chan time.Time {
	mu.Lock()
	var addr, left = pinnedAddr, pinnedFallbackAfter-time.Since(pinnedSeen)
	mu.Unlock()
	if addr == "" || left <= 0 { return nil }
	return time.After(left)
}

// requirePrimary fails when a pinned peer is set but is not among the peers
// that answered a request: headers and filters are synced from it unless it
// is away and the sync fell back to the other peers.
func requirePrimary(peers []*conn) error {
	var addr = currentPinned()
	if addr == "" || (len(peers) > 0 && peers[0].pinned) || pinnedAway() {
		return nil
	}
	return fmt.Errorf("pinned peer %s did not answer", addr)
}

// takeCandidates returns up to n candidate addresses not already connected,
// rebuilding the candidate list from the store and DNS seeds when it runs out.
func takeCandidates(n int) []string {
	var taken []string
	for round := 0; round < 2 && len(taken) < n; round++ {
		mu.Lock()
		var connected = make(map[string]bool, len(pool))
		for _, c := range pool {
			connected[c.addr] = true
		}
		for candNext < len(candidates) && len(taken) < n {
			var addr = candidates[candNext]
			candNext++
			if !connected[addr] { taken = append(taken, addr) }
		}
		var exhausted = candNext >= len(candidates)
		mu.Unlock()
		if !exhausted || len(taken) >= n { break }
		var fresh, err = peerCandidates(params, store, currentPinned())
		if err != nil {
			log.Printf("peer discovery: %v", err)
			break
		}
		mu.Lock()
		candidates = fresh
		candNext = 0
		mu.Unlock()
	}
	return taken
}

// connect dials one candidate and adds it to the pool, reporting whether it
// joined. A peer without the compact filters service is dropped after its
// services are stored, and so is one completing its handshake after the pool
// was filled by others. A dial abandoned through ctx is not held against the
// peer.
func connect(ctx context.Context, addr string, pinned bool) bool {
	var c, err = newConn(addr)
	if err != nil { return false }
	c.pinned = pinned
	var handshake, herr = c.dial(ctx, true)
	if herr != nil {
		if ctx.Err() != nil { return false }
		log.Printf("peer %s: connect failed: %v", addr, herr)
		if rerr := store.RecordPeerResult(c.host, c.port, false, 0); rerr != nil {
			log.Printf("record peer %s: %v", addr, rerr)
		}
		return false
	}
	var p = c.peer
	var flags = uint64(p.Services())
	if serr := store.UpsertPeer(storage.Peer{Host: c.host, Port: c.port, Services: flags, LatencyMs: handshake.Milliseconds()}); serr != nil {
		log.Printf("store peer %s: %v", addr, serr)
	}
	if flags&uint64(wire.SFNodeCF) == 0 {
		if pinned {
			log.Printf("pinned peer %s: no compact filter service (%s), disconnecting; it needs blockfilterindex=1 and peerblockfilters=1", addr, p.Services())
		} else {
			log.Printf("peer %s: no compact filter service (%s), disconnecting", addr, p.Services())
		}
		c.close()
		return false
	}
	p.QueueMessage(wire.NewMsgGetAddr(), nil)
	mu.Lock()
	if stopped {
		mu.Unlock()
		c.close()
		return false
	}
	if pinned && addr != pinnedAddr {
		mu.Unlock()
		log.Printf("peer %s: no longer the pinned peer, disconnecting", addr)
		c.close()
		return false
	}
	if !pinned && openSlotsLocked() <= 0 {
		mu.Unlock()
		log.Printf("peer %s: pool already full, disconnecting", addr)
		c.close()
		return false
	}
	pool = append(pool, c)
	var count = len(pool)
	wg.Add(1)
	mu.Unlock()
	var kind = "peer"
	if pinned { kind = "pinned peer" }
	log.Printf("%s %s connected: %s, height %d, services %s, handshake %s (%d connected)",
		kind, addr, p.UserAgent(), p.LastBlock(), p.Services(), handshake.Round(time.Millisecond), count)
	c.loadBloom()
	rebroadcast(c)
	logRelayMode()
	emit()
	wakeSync()
	go func() {
		defer wg.Done()
		<-c.quit
		mu.Lock()
		pool = slices.DeleteFunc(pool, func(other *conn) bool { return other == c })
		if c.pinned { pinnedSeen = time.Now() }
		var left = len(pool)
		mu.Unlock()
		log.Printf("%s %s disconnected (%d connected)", kind, addr, left)
		logRelayMode()
		emit()
		notifyPool()
	}()
	return true
}

// syncLoop syncs against one pool peer whenever a new block is announced, a
// peer connects, the pinned peer has been away long enough to fall back to
// the others, or the poll interval elapses. A failing peer is disconnected
// so the next round uses another one.
func syncLoop() {
	defer wg.Done()
	var fellBack = false
	for {
		select {
		case <-stop:
			return
		default:
		}
		var c = primary()
		if c == nil {
			select {
			case <-stop:
				return
			case <-wake:
			case <-fallbackTimer():
			}
			continue
		}
		if pinned := currentPinned(); pinned != "" && c.pinned == fellBack {
			fellBack = !c.pinned
			if fellBack {
				log.Printf("sync: pinned peer %s away for %s, syncing from %s", pinned, pinnedFallbackAfter, c.addr)
			} else {
				log.Printf("sync: syncing from pinned peer %s", pinned)
			}
		}
		mu.Lock()
		var seen = announced
		mu.Unlock()
		var err = syncOnce(c)
		mu.Lock()
		roundOK = err == nil
		if roundOK { syncedThrough = seen }
		mu.Unlock()
		report(StateIdle, chain.Height())
		if err != nil {
			log.Printf("sync: round failed: %v", err)
			if !pause(retryDelay) { return }
			continue
		}
		select {
		case <-stop:
			return
		case <-wake:
		case <-time.After(pollInterval):
		}
	}
}

// syncOnce brings headers up to the primary peer's tip, then filters and
// wallet transactions up to the header tip, and runs the queued rescans. The first filter header is
// majority proven once per run before the filter download starts. A primary
// failing the header sync is disconnected so the next round uses another.
func syncOnce(c *conn) error {
	if err := expirePending(); err != nil { return err }
	var before = chain.Height()
	report(StateHeaders, before)
	if _, err := syncHeaders(c); err != nil {
		c.drop("headers", err)
		return fmt.Errorf("headers: %w", err)
	}
	if chain.Height() != before {
		log.Printf("sync: headers synced to %d from %s", chain.Height(), c.addr)
	}
	RefreshFilterStart()
	var needsAnchor, err = anchorPending()
	if err != nil { return err }
	if needsAnchor && !anchorTried {
		report(StateFilters, FilterStart())
		var anchor, ok, err = proveFilterAnchor()
		if err != nil {
			return fmt.Errorf("prove filter header anchor: %w", err)
		}
		anchorTried = true
		if ok {
			SetFilterAnchor(anchor)
			log.Printf("sync: filter header anchor at height %d proven by peer majority: %s", FilterStart(), anchor)
		} else {
			log.Printf("sync: no peer answered the filter header anchor request; the filter peers will be trusted")
		}
	}
	var n, nerr = syncFilters()
	if nerr != nil {
		return fmt.Errorf("filters: %w", nerr)
	}
	if err := rescan(); err != nil {
		return fmt.Errorf("rescan: %w", err)
	}
	if n > 0 || chain.Height() != before {
		log.Printf("sync: wallet synced to tip %d (%d new filters)", chain.Height(), n)
	}
	return nil
}

// proveFilterAnchor asks up to anchorPeerLimit peers for the first filter
// header and returns the value reported by a strict majority. The connected
// peers are asked first, all at once; further candidates are then dialed
// anchorDialers at a time until enough votes are in. ok is false when no peer
// responded; the caller then falls back to the filter peers. Peers
// disagreeing without a majority are an error.
func proveFilterAnchor() (chainhash.Hash, bool, error) {
	var connected = filterPeers()
	var answered, votes = fanOut(connected, "filter header anchor", requestFilterAnchor)
	var inPool = make(map[string]bool)
	for _, c := range poolPeers() {
		inPool[c.addr] = true
	}
	mu.Lock()
	var list []string
	for _, addr := range candidates {
		if !inPool[addr] { list = append(list, addr) }
	}
	mu.Unlock()
	var votesMu sync.Mutex
	var enough = func() bool {
		votesMu.Lock()
		defer votesMu.Unlock()
		return len(votes) >= anchorPeerLimit
	}
	var work = make(chan string)
	var workers sync.WaitGroup
	var dialed = 0
	for range anchorDialers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for addr := range work {
				if enough() { continue }
				var anchor, err = dialAnchor(addr)
				if err != nil { continue }
				votesMu.Lock()
				votes = append(votes, anchor)
				dialed++
				votesMu.Unlock()
			}
		}()
	}
feed:
	for _, addr := range list {
		if enough() { break }
		select {
		case work <- addr:
		case <-stop:
			break feed
		}
	}
	close(work)
	workers.Wait()
	select {
	case <-stop:
		return chainhash.Hash{}, false, fmt.Errorf("stopped")
	default:
	}
	log.Printf("sync: filter header anchor votes: %d from connected peers, %d from dialed peers", len(answered), dialed)
	if len(votes) == 0 {
		return chainhash.Hash{}, false, nil
	}
	var anchor, err = MajorityAnchor(votes)
	if err != nil {
		return chainhash.Hash{}, false, err
	}
	return anchor, true, nil
}

// dialAnchor connects to one candidate just long enough to ask it for the
// filter header anchor.
func dialAnchor(addr string) (chainhash.Hash, error) {
	var c, err = newConn(addr)
	if err != nil { return chainhash.Hash{}, err }
	if _, err := c.dial(dialCtx, false); err != nil {
		_ = store.RecordPeerResult(c.host, c.port, false, 0)
		return chainhash.Hash{}, err
	}
	defer c.close()
	var flags = uint64(c.peer.Services())
	if err := store.UpdatePeerServices(c.host, c.port, flags); err != nil {
		log.Printf("store peer %s: %v", addr, err)
	}
	if flags&uint64(wire.SFNodeCF) == 0 {
		return chainhash.Hash{}, fmt.Errorf("no compact filter service")
	}
	var anchor, aerr = requestFilterAnchor(c)
	if aerr != nil {
		log.Printf("peer %s: filter header anchor: %v", addr, aerr)
	}
	return anchor, aerr
}

// peerCandidates builds the list of peers to fill the pool with: stored and
// freshly discovered peers in random order, without the pinned peer, which is
// dialed on its own. Stored peers that are known not to serve compact filters
// are skipped. An empty list is an error only without a pinned peer.
func peerCandidates(network *chaincfg.Params, db *storage.Store, pinned string) ([]string, error) {
	var list []string
	var seen = map[string]bool{pinned: true}
	var add = func(addr string) {
		if addr == "" || seen[addr] { return }
		seen[addr] = true
		list = append(list, addr)
	}
	var stored, err = db.Peers()
	if err != nil { return nil, err }
	for _, p := range stored {
		if p.Services != 0 && p.Services&uint64(wire.SFNodeCF) == 0 { continue }
		add(net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
	}
	for _, seed := range p2p.Seeds(network) {
		add(seed.String())
	}
	rand.Shuffle(len(list), func(i, j int) {
		list[i], list[j] = list[j], list[i]
	})
	if len(list) == 0 && pinned == "" {
		return nil, fmt.Errorf("no peer addresses for network %s (pass -peer or run a local node)", network.Name)
	}
	return list, nil
}

// splitHostPort splits a host:port address into its parts.
func splitHostPort(addr string) (string, uint16, error) {
	var host, port, err = net.SplitHostPort(addr)
	if err != nil { return "", 0, err }
	var num, err2 = strconv.Atoi(port)
	if err2 != nil { return "", 0, err2 }
	return host, uint16(num), nil
}
