// Package p2p dials Bitcoin P2P peers and performs the protocol handshake.
// Message serialisation, version negotiation and keepalive are handled by
// the battle-tested btcd peer package; this layer only wires it up for the
// wallet's SPV sync needs.
package p2p

import "context"
import "errors"
import "fmt"
import "net"
import "strconv"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/peer"
import "github.com/btcsuite/btcd/wire"

// HandshakeTimeout bounds the TCP dial and the version/verack exchange.
const HandshakeTimeout = 15 * time.Second

// OverlayDialTimeout bounds opening a connection to an onion peer through
// Tor or to an I2P peer through I2P: the onion service rendezvous and the
// I2P destination lookup and tunnels take far longer than a TCP dial.
// OverlayHandshakeTimeout bounds the version/verack exchange over the
// slower path.
const OverlayDialTimeout = 2 * time.Minute
const OverlayHandshakeTimeout = 45 * time.Second

// KeepAlive is how long a peer connection may stay silent, its keepalive
// probes unanswered, before the OS drops it: the first probe goes out after
// half of it, then keepAliveProbes probes, the last timing out at the end.
// A peer lost to a network change or a dead link is noticed within it rather
// than after the OS default of minutes, so a new peer is chosen sooner.
const KeepAlive = 30 * time.Second
const keepAliveProbes = 3

// PeerAddr is one candidate peer: an IP address and its TCP port.
type PeerAddr struct {
	Host string
	Port uint16
}

// String returns the dialable host:port form of the address.
func (a PeerAddr) String() string {
	return net.JoinHostPort(a.Host, strconv.Itoa(int(a.Port)))
}

// Dialer opens the connections to peers: Direct for addresses on the
// internet, the embedded Tor client for onion addresses.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Direct dials peers over plain TCP and probes the connections with TCP
// keepalives as KeepAlive sets.
var Direct Dialer = &net.Dialer{Timeout: HandshakeTimeout, KeepAliveConfig: keepAliveConfig()}

// Dial connects directly to the given peer address and completes the
// version/verack handshake, leaving the peer ready for message exchange. The
// listeners are installed before the connection starts so no early message
// is missed. The returned duration is the full handshake latency, dial
// included. The local peer advertises no services: it is a light client, and
// it asks the peer not to relay transactions.
func Dial(params *chaincfg.Params, address string, listeners peer.MessageListeners) (*peer.Peer, time.Duration, error) {
	return DialContext(context.Background(), params, Direct, address, listeners, false)
}

// DialContext is Dial through the dialer that gives up as soon as the
// context is cancelled, during the dial as well as during the handshake. An
// onion or I2P peer gets the longer overlay timeouts. relay sets whether the
// peer should announce the transactions it relays, the version message relay
// flag; without it only a BIP37 filterload turns relay on. The peer is asked
// for addrv2 messages, which carry onion addresses.
func DialContext(ctx context.Context, params *chaincfg.Params, dialer Dialer, address string, listeners peer.MessageListeners, relay bool) (*peer.Peer, time.Duration, error) {
	var started = time.Now()
	var dialTimeout, handshakeTimeout = HandshakeTimeout, HandshakeTimeout
	if host, _, err := net.SplitHostPort(address); err == nil && NetworkOf(host) != NetDirect {
		dialTimeout, handshakeTimeout = OverlayDialTimeout, OverlayHandshakeTimeout
	}
	var dialCtx, cancel = context.WithTimeout(ctx, dialTimeout)
	var conn, err = dialer.DialContext(dialCtx, "tcp", address)
	cancel()
	if err != nil {
		return nil, 0, fmt.Errorf("dial peer %s: %w", address, err)
	}
	var cfg = &peer.Config{
		UserAgentName:       "bitfyn",
		UserAgentVersion:    "0.1.0",
		ChainParams:         params,
		Services:            0,
		ProtocolVersion:     wire.AddrV2Version,
		DisableRelayTx:      !relay,
		DisableStallHandler: true,
		Listeners:           listeners,
		HostToNetAddress:    hostToNetAddress,
	}
	var p, perr = peer.NewOutboundPeer(cfg, address)
	if perr != nil {
		conn.Close()
		return nil, 0, fmt.Errorf("create peer: %w", perr)
	}
	p.AssociateConnection(conn)
	var deadline = time.Now().Add(handshakeTimeout)
	for time.Now().Before(deadline) {
		if p.VerAckReceived() {
			return p, time.Since(started), nil
		}
		if !p.Connected() {
			break
		}
		select {
		case <-ctx.Done():
			p.Disconnect()
			p.WaitForDisconnect()
			return nil, 0, fmt.Errorf("handshake with %s: %w", address, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	p.Disconnect()
	p.WaitForDisconnect()
	return nil, 0, fmt.Errorf("handshake with %s timed out", address)
}

// ErrNoCompactFilters is returned by Probe for a peer that does not serve
// BIP157 compact filters, which the wallet syncs from.
var ErrNoCompactFilters = errors.New("no compact filter service")

// Probe connects directly to the peer, completes the handshake and checks
// that the peer serves compact filters, as the sync needs, then disconnects.
// It gives up once the context is cancelled.
func Probe(ctx context.Context, params *chaincfg.Params, address string) error {
	var p, _, err = DialContext(ctx, params, Direct, address, peer.MessageListeners{}, false)
	if err != nil { return err }
	defer p.WaitForDisconnect()
	defer p.Disconnect()
	if p.Services()&wire.SFNodeCF == 0 {
		return fmt.Errorf("%s: %w", address, ErrNoCompactFilters)
	}
	return nil
}

// keepAliveConfig probes an idle connection after half of KeepAlive and then
// keepAliveProbes times over the other half, so an unanswered connection is
// dropped KeepAlive after it went silent.
func keepAliveConfig() net.KeepAliveConfig {
	return net.KeepAliveConfig{
		Enable:   true,
		Idle:     KeepAlive / 2,
		Interval: KeepAlive / 2 / keepAliveProbes,
		Count:    keepAliveProbes,
	}
}

// Seeds resolves every IP address advertised by the network's DNS seeds.
// The local test networks have no seeds and return an empty list.
func Seeds(params *chaincfg.Params) []PeerAddr {
	type seedHost struct {
		host string
		port uint16
	}
	var seeds []seedHost
	switch params.Net {
	case chaincfg.MainNetParams.Net:
		seeds = []seedHost{
			{"seed.bitcoin.sipa.be", 8333},
			{"dnsseed.bluematt.me", 8333},
			{"dnsseed.bitcoin.dashjr.org", 8333},
			{"seed.bitcoin.jonasschnelli.ch", 8333},
			{"bitcoin.sprovoost.nl", 8333},
			{"dnsseed.emzy.de", 8333},
			{"seed.bitcoin.wiz.biz", 8333},
			{"seed.bitcoinstats.com", 8333},
		}
	case chaincfg.TestNet3Params.Net:
		seeds = []seedHost{
			{"testnet-seed.bitcoin.jonasschnelli.ch", 18333},
			{"seed.tbtc.petertodd.org", 18333},
			{"testnet-seed.bluematt.me", 18333},
			{"testnet-seed.bitcoin.sprovoost.nl", 18333},
			{"testnet-seed.bitcoin.wiz.biz", 18333},
		}
	case chaincfg.TestNet4Params.Net:
		seeds = []seedHost{
			{"seed.testnet4.bitcoin.sprovoost.nl", 48333},
			{"seed.testnet4.wiz.biz", 48333},
		}
	case chaincfg.SigNetParams.Net:
		seeds = []seedHost{
			{"seed.signet.bitcoin.sprovoost.nl", 38333},
			{"seed.signet.achownodes.xyz", 38333},
		}
	default:
		return nil
	}
	var out []PeerAddr
	for _, seed := range seeds {
		var ips, err = net.LookupHost(seed.host)
		if err != nil { continue }
		for _, ip := range ips {
			out = append(out, PeerAddr{Host: ip, Port: seed.port})
		}
	}
	return out
}
