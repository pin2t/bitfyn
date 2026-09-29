package sync

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
// when majority proving the filter header chain anchor.
const anchorPeerLimit = 10

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
// peers, the current activity and the block height that activity has reached.
type Status struct {
	State  State
	Peers  int
	Height int32
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
var wake chan struct{}
var poolChanged chan struct{}
var explicitPeer string
var candidates []string
var candNext int
var anchorTried bool

// Start opens the wallet database state and begins syncing in the
// background: it keeps TargetPeers peers connected, syncs headers and filters
// and then stays connected to follow new blocks. Every change of the
// connection count or sync activity is reported through onStatus, from any
// goroutine. explicit is an optional host:port peer that is tried first.
func Start(network *chaincfg.Params, db *storage.Store, explicit string, onStatus func(Status)) error {
	if explicit != "" {
		if _, _, err := net.SplitHostPort(explicit); err != nil {
			return fmt.Errorf("peer address %q must include a port", explicit)
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
	wake = make(chan struct{}, 1)
	poolChanged = make(chan struct{}, 1)
	explicitPeer = explicit
	candidates = nil
	candNext = 0
	anchorTried = false
	wg.Add(2)
	mu.Unlock()
	emit()
	go maintain()
	go syncLoop()
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
	mu.Lock()
	var status = Status{State: activity, Peers: len(pool), Height: height}
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
// candidates concurrently.
func maintain() {
	defer wg.Done()
	for {
		select {
		case <-stop:
			return
		default:
		}
		var need = TargetPeers - poolSize()
		var wait = retryDelay
		if need > 0 {
			var addrs = takeCandidates(need)
			var dials sync.WaitGroup
			for _, addr := range addrs {
				dials.Add(1)
				go func() {
					defer dials.Done()
					connect(addr)
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

// primary returns the connected peer that drives the header sync.
func primary() *conn {
	var peers = livePeers(poolPeers())
	if len(peers) == 0 { return nil }
	return peers[0]
}

// filterPeers returns up to TargetPeers connected peers to download and
// cross-check filters from, the primary first.
func filterPeers() []*conn {
	var peers = livePeers(poolPeers())
	return peers[:min(len(peers), TargetPeers)]
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
		var fresh, err = peerCandidates(params, store, explicitPeer)
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

// connect dials one candidate and adds it to the pool. A peer without the
// compact filters service is dropped after its services are stored.
func connect(addr string) {
	var c, err = newConn(addr)
	if err != nil { return }
	handshake, err := c.dial()
	if err != nil {
		log.Printf("peer %s: connect failed: %v", addr, err)
		if rerr := store.RecordPeerResult(c.host, c.port, false, 0); rerr != nil {
			log.Printf("record peer %s: %v", addr, rerr)
		}
		return
	}
	var p = c.peer
	var flags = uint64(p.Services())
	if serr := store.UpsertPeer(storage.Peer{Host: c.host, Port: c.port, Services: flags, LatencyMs: handshake.Milliseconds()}); serr != nil {
		log.Printf("store peer %s: %v", addr, serr)
	}
	if flags&uint64(wire.SFNodeCF) == 0 {
		log.Printf("peer %s: no compact filter service (%s), disconnecting", addr, p.Services())
		c.close()
		return
	}
	p.QueueMessage(wire.NewMsgGetAddr(), nil)
	mu.Lock()
	if stopped {
		mu.Unlock()
		c.close()
		return
	}
	pool = append(pool, c)
	var count = len(pool)
	wg.Add(1)
	mu.Unlock()
	log.Printf("peer %s connected: %s, height %d, services %s, handshake %s (%d connected)",
		addr, p.UserAgent(), p.LastBlock(), p.Services(), handshake.Round(time.Millisecond), count)
	emit()
	wakeSync()
	go func() {
		defer wg.Done()
		<-c.quit
		mu.Lock()
		pool = slices.DeleteFunc(pool, func(other *conn) bool { return other == c })
		var left = len(pool)
		mu.Unlock()
		log.Printf("peer %s disconnected (%d connected)", addr, left)
		emit()
		notifyPool()
	}()
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
			if !pause(time.Second) { return }
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
	if NeedsAnchor() && !anchorTried {
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
	var n, err = syncFilters()
	if err != nil {
		return fmt.Errorf("filters: %w", err)
	}
	if n > 0 || chain.Height() != before {
		log.Printf("sync: wallet synced to tip %d (%d new filters)", chain.Height(), n)
	}
	return nil
}

// proveFilterAnchor asks up to anchorPeerLimit peers for the first filter
// header and returns the value reported by a strict majority. ok is false
// when no peer responded; the caller then falls back to trusting the first
// filter peer. Peers disagreeing without a majority are an error.
func proveFilterAnchor() (chainhash.Hash, bool, error) {
	mu.Lock()
	var list = append([]string(nil), candidates...)
	mu.Unlock()
	var votes []chainhash.Hash
	for _, addr := range list {
		if len(votes) >= anchorPeerLimit { break }
		select {
		case <-stop:
			return chainhash.Hash{}, false, fmt.Errorf("stopped")
		default:
		}
		var pc, perr = newConn(addr)
		if perr != nil { continue }
		if _, derr := pc.dial(); derr != nil {
			_ = store.RecordPeerResult(pc.host, pc.port, false, 0)
			continue
		}
		var flags = uint64(pc.peer.Services())
		if serr := store.UpdatePeerServices(pc.host, pc.port, flags); serr != nil {
			log.Printf("store peer %s: %v", addr, serr)
		}
		if flags&uint64(wire.SFNodeCF) == 0 {
			pc.close()
			continue
		}
		var anchor, aerr = requestFilterAnchor(pc)
		pc.close()
		if aerr != nil {
			log.Printf("peer %s: filter header anchor: %v", addr, aerr)
			continue
		}
		votes = append(votes, anchor)
	}
	if len(votes) == 0 {
		return chainhash.Hash{}, false, nil
	}
	var anchor, err = MajorityAnchor(votes)
	if err != nil {
		return chainhash.Hash{}, false, err
	}
	return anchor, true, nil
}

// peerCandidates builds the ordered list of peers to try: the explicit
// address first when given, then stored and freshly discovered peers in random
// order. Stored peers that are known not to serve compact filters are skipped.
func peerCandidates(network *chaincfg.Params, db *storage.Store, explicit string) ([]string, error) {
	var list []string
	var seen = make(map[string]bool)
	var add = func(addr string) {
		if addr == "" || seen[addr] { return }
		seen[addr] = true
		list = append(list, addr)
	}
	var start = 0
	if explicit != "" {
		add(explicit)
		start = 1
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
	rand.Shuffle(len(list)-start, func(i, j int) {
		list[start+i], list[start+j] = list[start+j], list[start+i]
	})
	if len(list) == 0 {
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
