package sync

import "context"
import "math/rand/v2"
import "net"
import "path/filepath"
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
	candidates = nil
	candNext = 0
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		stopped = true
		close(stop)
		cancelDials()
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
