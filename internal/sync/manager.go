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
// and the wallet balance in satoshis: confirmed, and the change pending
// unconfirmed transactions make to it.
type Status struct {
	State   State
	Peers   int
	Height  int32
	Balance int64
	Pending int64
}

// Bars returns the connectivity level from 0 to TargetPeers: one per
// connected peer.
func (s Status) Bars() int {
	return min(s.Peers, TargetPeers)
}

// String is the status line: not connected, connected, or the syncing stage
// with its current block number.
func (s Status) String() string {
	if s.Peers == 0 {
		return "Not connected"
	}
	switch s.State {
	case StateHeaders:
		return fmt.Sprintf("Syncing headers: block %d", s.Height)
	case StateFilters:
		return fmt.Sprintf("Syncing filters: block %d", s.Height)
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
// from it. The other peers only cross-check its filters.
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
	candidates = nil
	candNext = 0
	anchorTried = false
	wg.Add(2)
	if pinned != "" { wg.Add(1) }
	mu.Unlock()
	emit()
	go maintain()
	go syncLoop()
	if pinned != "" { go keepPinned() }
	return nil
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
	var status = Status{State: activity, Peers: len(pool), Height: height, Balance: confirmed, Pending: pending}
	var cb = notify
	var live = !stopped
	mu.Unlock()
	if cb != nil && live {
		cb(status)
	}
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

// maintain keeps the pool filled up to TargetPeers, dialing the next
// candidates concurrently. While a pinned peer is set, one slot is kept for
// it; keepPinned dials it on its own.
func maintain() {
	defer wg.Done()
	for {
		select {
		case <-stop:
			return
		default:
		}
		var need = TargetPeers - poolSize()
		if pinnedAddr != "" && pinnedConn() == nil { need-- }
		var wait = retryDelay
		if need > 0 {
			var addrs = takeCandidates(need)
			var dials sync.WaitGroup
			for _, addr := range addrs {
				dials.Add(1)
				go func() {
					defer dials.Done()
					connect(addr, false)
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

// keepPinned keeps the pinned peer connected: it dials it, waits for the
// connection to drop and dials again. It backs off while the peer is
// unreachable or keeps dropping the connection soon after the handshake, as
// a node with full inbound slots does when it evicts its newest peer.
func keepPinned() {
	defer wg.Done()
	var backoff = retryDelay
	for {
		if connect(pinnedAddr, true) {
			var c = pinnedConn()
			var since = time.Now()
			if c != nil {
				select {
				case <-stop:
					return
				case <-c.quit:
				}
			}
			var lasted = time.Since(since)
			if lasted >= pinnedStableAfter {
				backoff = retryDelay
				continue
			}
			log.Printf("pinned peer %s dropped the connection after %s", pinnedAddr, lasted.Round(time.Millisecond))
		}
		log.Printf("pinned peer %s: retrying in %s", pinnedAddr, backoff)
		if !pause(backoff) { return }
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
// peer set, only the pinned peer qualifies: nil means waiting for it.
func primary() *conn {
	if pinnedAddr != "" {
		return pinnedConn()
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

// requirePrimary fails when a pinned peer is set but is not among the peers
// that answered a request: headers and filters are always synced from it.
func requirePrimary(peers []*conn) error {
	if pinnedAddr == "" || (len(peers) > 0 && peers[0].pinned) {
		return nil
	}
	return fmt.Errorf("pinned peer %s did not answer", pinnedAddr)
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
		var fresh, err = peerCandidates(params, store, pinnedAddr)
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
// services are stored.
func connect(addr string, pinned bool) bool {
	var c, err = newConn(addr)
	if err != nil { return false }
	c.pinned = pinned
	handshake, err := c.dial(true)
	if err != nil {
		log.Printf("peer %s: connect failed: %v", addr, err)
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
	pool = append(pool, c)
	var count = len(pool)
	wg.Add(1)
	mu.Unlock()
	var kind = "peer"
	if pinned { kind = "pinned peer" }
	log.Printf("%s %s connected: %s, height %d, services %s, handshake %s (%d connected)",
		kind, addr, p.UserAgent(), p.LastBlock(), p.Services(), handshake.Round(time.Millisecond), count)
	c.loadBloom()
	logRelayMode()
	emit()
	wakeSync()
	go func() {
		defer wg.Done()
		<-c.quit
		mu.Lock()
		pool = slices.DeleteFunc(pool, func(other *conn) bool { return other == c })
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
// peer connects, or the poll interval elapses. A failing peer is disconnected
// so the next round uses another one.
func syncLoop() {
	defer wg.Done()
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
			}
			continue
		}
		var err = syncOnce(c)
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
// wallet transactions up to the header tip. The first filter header is
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
	n, err := syncFilters()
	if err != nil {
		return fmt.Errorf("filters: %w", err)
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
	if _, err := c.dial(false); err != nil {
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
