package sync

import "context"
import "encoding/base32"
import crand "crypto/rand"
import "fmt"
import "math/rand/v2"
import "net"
import "path/filepath"
import "slices"
import "strings"
import "strconv"
import "testing"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/p2p"
import "bitfyn/internal/storage"

// fakePeer listens on a local port and completes the handshake as a node
// serving compact filters, ignoring every later message. It speaks the wire
// protocol itself: a btcd peer in the same process as the dialer would take
// the dialer's nonce for its own and drop the connection to itself.
func fakePeer(t *testing.T) string {
	t.Helper()
	return listen(t, func(c net.Conn) {
		t.Cleanup(func() { c.Close() })
		go answerHandshake(c)
	})
}

// answerHandshake answers the version message with a version and a verack.
func answerHandshake(c net.Conn) {
	var network = chaincfg.RegressionNetParams.Net
	var services = wire.SFNodeNetwork | wire.SFNodeCF
	for {
		var msg, _, err = wire.ReadMessage(c, wire.FeeFilterVersion, network)
		if err != nil { return }
		if _, ok := msg.(*wire.MsgVersion); !ok { continue }
		var addr = wire.NewNetAddressIPPort(net.IPv4(127, 0, 0, 1), 0, services)
		var version = wire.NewMsgVersion(addr, addr, rand.Uint64(), 0)
		version.ProtocolVersion = int32(wire.FeeFilterVersion)
		version.Services = services
		if wire.WriteMessage(c, version, wire.FeeFilterVersion, network) != nil { return }
		if wire.WriteMessage(c, wire.NewMsgVerAck(), wire.FeeFilterVersion, network) != nil { return }
	}
}

// blackHole listens on a local port, accepts connections and never answers,
// as an unreachable node behind a filtering firewall looks while dialing.
func blackHole(t *testing.T) string {
	t.Helper()
	return listen(t, func(c net.Conn) { t.Cleanup(func() { c.Close() }) })
}

// closedPort is a local address refusing connections.
func closedPort(t *testing.T) string {
	t.Helper()
	var l, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatalf("listen: %v", err) }
	var addr = l.Addr().String()
	l.Close()
	return addr
}

// listen accepts connections on a local port and hands each to serve.
func listen(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	var l, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatalf("listen: %v", err) }
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			var c, err = l.Accept()
			if err != nil { return }
			serve(c)
		}
	}()
	return l.Addr().String()
}

// startFill prepares the manager state fillPool runs on, with the addresses
// as the only stored peers, and disconnects every joined peer at the end.
func startFill(t *testing.T, addrs []string) *storage.Store {
	t.Helper()
	resetSync()
	var db, err = storage.Open(filepath.Join(t.TempDir(), "w.db"), "")
	if err != nil { t.Fatalf("Open: %v", err) }
	for _, addr := range addrs {
		var host, port, _ = net.SplitHostPort(addr)
		var num, _ = strconv.Atoi(port)
		if err := db.UpsertPeer(storage.Peer{Host: host, Port: uint16(num)}); err != nil {
			t.Fatalf("UpsertPeer: %v", err)
		}
	}
	if err := Init(&chaincfg.RegressionNetParams, db); err != nil { t.Fatalf("Init: %v", err) }
	mu.Lock()
	pool = nil
	stopped = false
	stop = make(chan struct{})
	dialCtx, cancelDials = context.WithCancel(context.Background())
	pinnedAddr = ""
	overlays = map[p2p.Network]p2p.Dialer{}
	refill = false
	candidates = nil
	candNext = 0
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		stopped = true
		close(stop)
		cancelDials()
		overlays = map[p2p.Network]p2p.Dialer{}
		var open = append([]*conn(nil), pool...)
		mu.Unlock()
		for _, c := range open {
			c.peer.Disconnect()
		}
		wg.Wait()
		db.Close()
		resetSync()
	})
	return db
}

// TestFillPool checks that the parallel first connect fills the pool with
// exactly TargetPeers peers even with more reachable peers than that, and
// long before a handshake timeout although dialers get stuck on silent
// hosts. The abandoned dials are not recorded as peer failures.
func TestFillPool(t *testing.T) {
	var good = []string{fakePeer(t), fakePeer(t), fakePeer(t), fakePeer(t)}
	var silent = []string{blackHole(t), blackHole(t), blackHole(t), blackHole(t)}
	var addrs = append(append(append([]string{}, good...), silent...), closedPort(t))
	var db = startFill(t, addrs)
	var started = time.Now()
	fillPool()
	if elapsed := time.Since(started); elapsed >= p2p.HandshakeTimeout/2 {
		t.Fatalf("fillPool took %s", elapsed)
	}
	if n := poolSize(); n != TargetPeers {
		t.Fatalf("%d peers connected, want %d", n, TargetPeers)
	}
	var stored, err = db.Peers()
	if err != nil { t.Fatalf("Peers: %v", err) }
	for _, p := range stored {
		var addr = net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
		for _, s := range silent {
			if addr == s && p.FailCount != 0 {
				t.Errorf("abandoned dial to %s recorded as a failure", addr)
			}
		}
	}
	mu.Lock()
	var left = len(candidates) - candNext
	mu.Unlock()
	if left < 0 || len(candidates) != len(addrs) {
		t.Fatalf("candidates %d, next %d; want the %d dialed ones kept", len(candidates), candNext, len(addrs))
	}
}

// TestFillPoolPinnedSlot checks that a slot stays free for a pinned peer
// that is not connected.
func TestFillPoolPinnedSlot(t *testing.T) {
	startFill(t, []string{fakePeer(t), fakePeer(t), fakePeer(t), fakePeer(t)})
	pinnedAddr = closedPort(t)
	fillPool()
	if n := poolSize(); n != TargetPeers-1 {
		t.Fatalf("%d peers connected, want %d with a slot kept for the pinned peer", n, TargetPeers-1)
	}
}

// TestFillPoolUnreachable checks that fillPool gives up once every candidate
// failed, leaving the retries to maintain.
func TestFillPoolUnreachable(t *testing.T) {
	startFill(t, []string{closedPort(t), closedPort(t)})
	fillPool()
	if n := poolSize(); n != 0 {
		t.Fatalf("%d peers connected, want none", n)
	}
}

// TestSetPinned pins the running sync to a peer, then to another one, then
// unpins it: each pinned peer gets connected as the pinned peer, the one
// pinned before is disconnected, and an address without a port is refused.
func TestSetPinned(t *testing.T) {
	startFill(t, nil)
	var connected = func(want string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			var peers = livePeers(poolPeers())
			var c = pinnedConn()
			if want == "" && len(peers) == 0 { return }
			if want != "" && c != nil && c.addr == want && len(peers) == 1 { return }
		}
		t.Fatalf("pinned peer %q not the only peer connected; pinned %q", want, currentPinned())
	}
	if err := SetPinned("192.0.2.1"); err == nil {
		t.Fatalf("SetPinned accepted an address without a port")
	}
	var first = fakePeer(t)
	if err := SetPinned(first); err != nil { t.Fatalf("SetPinned: %v", err) }
	connected(first)
	var second = fakePeer(t)
	if err := SetPinned(second); err != nil { t.Fatalf("SetPinned: %v", err) }
	connected(second)
	if err := SetPinned(""); err != nil { t.Fatalf("SetPinned: %v", err) }
	connected("")
	if got := currentPinned(); got != "" {
		t.Fatalf("pinned peer %q after unpinning", got)
	}
}

// overlayDialer stands in for the Tor or I2P client: it connects each onion
// or I2P address to the local fake peer it maps to.
type overlayDialer map[string]string

func (d overlayDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var local, ok = d[addr]
	if !ok { return nil, fmt.Errorf("unknown onion address %s", addr) }
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, local)
}

// overlayPeers maps count fake peers to addresses on the network, Tor or
// I2P, in the dialer, and returns the addresses.
func overlayPeers(t *testing.T, n p2p.Network, count int, dialer overlayDialer) []string {
	var out []string
	for range count {
		var key = make([]byte, 32)
		crand.Read(key)
		var host = p2p.OnionAddress(key)
		if n == p2p.NetI2P {
			host = strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)) + ".b32.i2p"
		}
		var addr = net.JoinHostPort(host, "8333")
		dialer[addr] = fakePeer(t)
		out = append(out, addr)
	}
	return out
}

// awaitPeers waits until exactly the peers at the addresses are connected.
func awaitPeers(t *testing.T, want []string) {
	t.Helper()
	var got []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		got = got[:0]
		for _, c := range livePeers(poolPeers()) {
			got = append(got, c.addr)
		}
		if len(got) == len(want) && !slices.ContainsFunc(want, func(a string) bool { return !slices.Contains(got, a) }) {
			return
		}
	}
	t.Fatalf("connected %v, want %v", got, want)
}

// TestTorMode switches the sync to Tor and back: Tor on disconnects the
// internet peers and dials nothing until the Tor client is ready, then only
// the onion peers; Tor off disconnects them and dials the internet peers
// again.
func TestTorMode(t *testing.T) {
	var direct = []string{fakePeer(t), fakePeer(t)}
	var dialer = overlayDialer{}
	var onions = overlayPeers(t, p2p.NetTor, 2, dialer)
	startFill(t, append(append([]string{}, direct...), onions...))
	fillPool()
	awaitPeers(t, direct)
	SetOverlay(p2p.NetTor, true, nil)
	awaitPeers(t, nil)
	if n := openSlots(); n != 0 {
		t.Fatalf("%d open slots while Tor is not ready, want none", n)
	}
	fillPool()
	awaitPeers(t, nil)
	SetOverlay(p2p.NetTor, true, dialer)
	fillPool()
	awaitPeers(t, onions)
	SetOverlay(p2p.NetTor, false, nil)
	awaitPeers(t, nil)
	fillPool()
	awaitPeers(t, direct)
}

// TestTorAndI2P turns I2P on, then Tor too, then I2P off: only the I2P
// peers are dialed, then the onion peers join them, then the I2P peers are
// disconnected and only the onion peers stay. The status names the
// networks.
func TestTorAndI2P(t *testing.T) {
	var direct = []string{fakePeer(t)}
	var dialer = overlayDialer{}
	var onions = overlayPeers(t, p2p.NetTor, 1, dialer)
	var i2ps = overlayPeers(t, p2p.NetI2P, 1, dialer)
	startFill(t, append(append(append([]string{}, direct...), onions...), i2ps...))
	SetOverlay(p2p.NetI2P, true, dialer)
	fillPool()
	awaitPeers(t, i2ps)
	SetOverlay(p2p.NetTor, true, dialer)
	fillPool()
	awaitPeers(t, append(append([]string{}, onions...), i2ps...))
	mu.Lock()
	var via = viaLocked()
	mu.Unlock()
	if via != "Tor and I2P" {
		t.Fatalf("via %q, want Tor and I2P", via)
	}
	SetOverlay(p2p.NetI2P, false, nil)
	awaitPeers(t, onions)
	if got := (Status{Peers: 0, Via: "Tor"}).String(); got != "Connecting over Tor..." {
		t.Fatalf("status %q", got)
	}
}
