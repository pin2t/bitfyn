// Package p2p dials Bitcoin P2P peers and performs the protocol handshake.
// Message serialisation, version negotiation and keepalive are handled by
// the battle-tested btcd peer package; this layer only wires it up for the
// wallet's SPV sync needs.
package p2p

import "fmt"
import "net"
import "strconv"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/peer"
import "github.com/btcsuite/btcd/wire"

// HandshakeTimeout bounds the TCP dial and the version/verack exchange.
const HandshakeTimeout = 15 * time.Second

// PeerAddr is one candidate peer: an IP address and its TCP port.
type PeerAddr struct {
	Host string
	Port uint16
}

// String returns the dialable host:port form of the address.
func (a PeerAddr) String() string {
	return net.JoinHostPort(a.Host, strconv.Itoa(int(a.Port)))
}

// Dial connects to the given peer address and completes the version/verack
// handshake, leaving the peer ready for message exchange. The listeners are
// installed before the connection starts so no early message is missed. The
// returned duration is the full handshake latency, dial included. The local
// peer advertises no services: it is a light client.
func Dial(params *chaincfg.Params, address string, listeners peer.MessageListeners) (*peer.Peer, time.Duration, error) {
	var started = time.Now()
	var conn, err = net.DialTimeout("tcp", address, HandshakeTimeout)
	if err != nil {
		return nil, 0, fmt.Errorf("dial peer %s: %w", address, err)
	}
	var cfg = &peer.Config{
		UserAgentName:       "bitfyn",
		UserAgentVersion:    "0.1.0",
		ChainParams:         params,
		Services:            0,
		ProtocolVersion:     wire.FeeFilterVersion,
		DisableRelayTx:      true,
		DisableStallHandler: true,
		Listeners:           listeners,
	}
	p, err := peer.NewOutboundPeer(cfg, address)
	if err != nil {
		conn.Close()
		return nil, 0, fmt.Errorf("create peer: %w", err)
	}
	p.AssociateConnection(conn)
	var deadline = time.Now().Add(HandshakeTimeout)
	for time.Now().Before(deadline) {
		if p.VerAckReceived() {
			return p, time.Since(started), nil
		}
		if !p.Connected() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.Disconnect()
	p.WaitForDisconnect()
	return nil, 0, fmt.Errorf("handshake with %s timed out", address)
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
