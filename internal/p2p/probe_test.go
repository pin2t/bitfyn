package p2p

import "context"
import "errors"
import "math/rand/v2"
import "net"
import "testing"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/wire"

// TestProbe checks that a peer serving compact filters passes the probe, one
// without them fails with ErrNoCompactFilters and a closed port fails to
// connect.
func TestProbe(t *testing.T) {
	var params = &chaincfg.RegressionNetParams
	if err := Probe(context.Background(), params, fakeNode(t, wire.SFNodeNetwork|wire.SFNodeCF)); err != nil {
		t.Fatalf("probe of a compact filter peer: %v", err)
	}
	var err = Probe(context.Background(), params, fakeNode(t, wire.SFNodeNetwork))
	if !errors.Is(err, ErrNoCompactFilters) {
		t.Fatalf("probe of a peer without compact filters = %v, want ErrNoCompactFilters", err)
	}
	var l, lerr = net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil { t.Fatalf("listen: %v", lerr) }
	var closed = l.Addr().String()
	l.Close()
	if err := Probe(context.Background(), params, closed); err == nil {
		t.Fatalf("probe of a closed port succeeded")
	}
}

// fakeNode listens on a local port and answers the handshake as a regtest
// node with the services. It speaks the wire protocol itself: a btcd peer in
// the same process would take the dialer's nonce for its own.
func fakeNode(t *testing.T, services wire.ServiceFlag) string {
	t.Helper()
	var l, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatalf("listen: %v", err) }
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			var c, err = l.Accept()
			if err != nil { return }
			t.Cleanup(func() { c.Close() })
			go answerVersion(c, services)
		}
	}()
	return l.Addr().String()
}

// answerVersion answers the version message with a version and a verack.
func answerVersion(c net.Conn, services wire.ServiceFlag) {
	var network = chaincfg.RegressionNetParams.Net
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
