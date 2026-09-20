// Package p2p dials Bitcoin P2P peers and performs the protocol handshake.
// Message serialisation, version negotiation and keepalive are handled by
// the battle-tested btcd peer package; this layer only wires it up for the
// wallet's SPV sync needs.
package p2p

import "fmt"
import "net"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/peer"
import "github.com/btcsuite/btcd/wire"

// HandshakeTimeout bounds the TCP dial and the version/verack exchange.
const HandshakeTimeout = 15 * time.Second

// Dial connects to the given peer address and completes the version/verack
// handshake, leaving the peer ready for message exchange. The listeners are
// installed before the connection starts so no early message is missed.
func Dial(params *chaincfg.Params, address string, listeners peer.MessageListeners) (*peer.Peer, error) {
	var conn, err = net.DialTimeout("tcp", address, HandshakeTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial peer %s: %w", address, err)
	}
	var cfg = &peer.Config{
		UserAgentName:       "bitfyn",
		UserAgentVersion:    "0.1.0",
		ChainParams:         params,
		Services:            wire.SFNodeNetwork | wire.SFNodeWitness,
		ProtocolVersion:     wire.FeeFilterVersion,
		DisableRelayTx:      true,
		DisableStallHandler: true,
		Listeners:           listeners,
	}
	p, err := peer.NewOutboundPeer(cfg, address)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("create peer: %w", err)
	}
	p.AssociateConnection(conn)
	var deadline = time.Now().Add(HandshakeTimeout)
	for time.Now().Before(deadline) {
		if p.VerAckReceived() {
			return p, nil
		}
		if !p.Connected() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.Disconnect()
	p.WaitForDisconnect()
	return nil, fmt.Errorf("handshake with %s timed out", address)
}

// ResolveAddress returns the peer address to dial for a network. An explicit
// host:port is returned unchanged; otherwise a well-known DNS seed for the
// network is resolved and its first IP address is used. The local test
// networks fall back to their default localhost ports.
func ResolveAddress(params *chaincfg.Params, address string) (string, error) {
	if address != "" {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return "", fmt.Errorf("peer address %q must include a port", address)
		}
		return address, nil
	}
	var host, port string
	switch params.Net {
	case chaincfg.MainNetParams.Net:
		host, port = "seed.bitcoin.sipa.be", "8333"
	case chaincfg.TestNet3Params.Net:
		host, port = "testnet-seed.bitcoin.jonasschnelli.ch", "18333"
	case chaincfg.RegressionNetParams.Net:
		return "127.0.0.1:18444", nil
	case chaincfg.SimNetParams.Net:
		return "127.0.0.1:18555", nil
	default:
		return "", fmt.Errorf("no peer seed for network %s", params.Name)
	}
	var ips, err = net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("resolve seed %s: %w", host, err)
	}
	return net.JoinHostPort(ips[0], port), nil
}
