package p2p

import "crypto/sha3"
import "encoding/base32"
import "fmt"
import "net"
import "strings"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/wire"

// onionVersion is the version byte of a v3 onion address, and onionLength
// the length of its name without the ".onion" suffix: the base32 encoding of
// the 32-byte service key, a 2-byte checksum and the version.
const onionVersion = 3
const onionLength = 56

// IsOnion reports whether the host is a v3 onion service address.
func IsOnion(host string) bool {
	var _, err = onionKey(host)
	return err == nil
}

// OnionAddress is the v3 onion address of the 32-byte service key, as
// rend-spec-v3 and BIP155 define it: the key, its checksum and the version
// in lower case base32, then ".onion".
func OnionAddress(key []byte) string {
	var raw = make([]byte, 0, 35)
	raw = append(raw, key...)
	raw = append(raw, onionChecksum(key)...)
	raw = append(raw, onionVersion)
	return strings.ToLower(base32.StdEncoding.EncodeToString(raw)) + ".onion"
}

// onionKey decodes the 32-byte service key of a v3 onion address, checking
// its version and checksum.
func onionKey(host string) ([]byte, error) {
	var name, ok = strings.CutSuffix(strings.ToLower(host), ".onion")
	if !ok || len(name) != onionLength {
		return nil, fmt.Errorf("%q is not a v3 onion address", host)
	}
	var raw, err = base32.StdEncoding.DecodeString(strings.ToUpper(name))
	if err != nil {
		return nil, fmt.Errorf("%q is not a v3 onion address: %w", host, err)
	}
	var key = raw[:32]
	if raw[34] != onionVersion || string(raw[32:34]) != string(onionChecksum(key)) {
		return nil, fmt.Errorf("%q has a wrong onion checksum or version", host)
	}
	return key, nil
}

// onionChecksum is the 2-byte checksum of a v3 onion address of the key.
func onionChecksum(key []byte) []byte {
	var h = sha3.New256()
	h.Write([]byte(".onion checksum"))
	h.Write(key)
	h.Write([]byte{onionVersion})
	return h.Sum(nil)[:2]
}

// hostToNetAddress is the peer address the version message names: the
// service key of an onion host, which btcd then leaves out, or the IP.
func hostToNetAddress(host string, port uint16, services wire.ServiceFlag) (*wire.NetAddressV2, error) {
	if key, err := onionKey(host); err == nil {
		return wire.NetAddressV2FromBytes(time.Now(), services, key, port), nil
	}
	var ip = net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("peer host %q is neither an IP nor an onion address", host)
	}
	return wire.NetAddressV2FromBytes(time.Now(), services, ip, port), nil
}

// OnionSeeds returns the built-in onion peers of the network to start from
// over Tor: long running mainnet nodes serving compact filters. Other
// networks have none.
func OnionSeeds(params *chaincfg.Params) []PeerAddr {
	if params.Net != chaincfg.MainNetParams.Net { return nil }
	return append([]PeerAddr(nil), onionSeeds...)
}
