package p2p

import "encoding/base32"
import "strings"
import "github.com/btcsuite/btcd/chaincfg"

// Network is how a peer is reached: directly over the internet, or as an
// onion service over Tor, or as an I2P destination over I2P.
type Network int

const NetDirect Network = 0
const NetTor Network = 1
const NetI2P Network = 2

// String is the network's name.
func (n Network) String() string {
	switch n {
	case NetTor:
		return "Tor"
	case NetI2P:
		return "I2P"
	default:
		return "direct"
	}
}

// NetworkOf tells which network the peer host is on.
func NetworkOf(host string) Network {
	switch {
	case IsOnion(host):
		return NetTor
	case IsI2P(host):
		return NetI2P
	default:
		return NetDirect
	}
}

// State is how far the client of an anonymity network, Tor or I2P, got: it
// starts, is ready to dial peers, or failed and retries.
type State int

const StateStarting State = 0
const StateReady State = 1
const StateFailed State = 2

// i2pLength is the length of an I2P b32 name without the ".b32.i2p"
// suffix: the base32 encoding, unpadded, of the SHA-256 hash of the
// destination.
const i2pLength = 52

// IsI2P reports whether the host is an I2P b32 address.
func IsI2P(host string) bool {
	var name, ok = strings.CutSuffix(strings.ToLower(host), ".b32.i2p")
	if !ok || len(name) != i2pLength { return false }
	var raw, err = base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(name))
	return err == nil && len(raw) == 32
}

// I2PSeeds returns the built-in I2P peers of the network to start from over
// I2P: mainnet nodes serving compact filters. Other networks have none.
func I2PSeeds(params *chaincfg.Params) []PeerAddr {
	if params.Net != chaincfg.MainNetParams.Net { return nil }
	return append([]PeerAddr(nil), i2pSeeds...)
}
