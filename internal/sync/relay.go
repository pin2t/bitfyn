package sync

import "log"
import "strconv"
import "sync"
import "sync/atomic"
import "github.com/btcsuite/btcd/wire"

// Every pool peer is asked to relay transactions. A peer serving BIP37
// filters then only announces transactions matching the wallet filter; the
// others announce every transaction they relay. While at least one bloom
// peer is connected, only bloom peers are listened to. Without any, the
// wallet falls back to downloading every relayed transaction from one peer,
// the primary, and checking each one itself: it costs full node transaction
// bandwidth but works with every node and reveals nothing about the wallet.

// relayLogEvery is how many checked relayed transactions are counted
// between two log lines.
const relayLogEvery = 1000

var relayMu sync.Mutex
var relayMode string
var relayedTxs atomic.Int64
var relayedBytes atomic.Int64

// countRelayed counts one relayed transaction checked against the wallet and
// logs the running totals now and then.
func countRelayed(c *conn, tx *wire.MsgTx) {
	var size = relayedBytes.Add(int64(tx.SerializeSize()))
	if n := relayedTxs.Add(1); n%relayLogEvery == 0 {
		log.Printf("tx relay: %d relayed transactions checked (%d KB), the latest from %s", n, size/1024, c.addr)
	}
}

// relaySource reports whether the transactions announced by the connection
// are fetched, given the connected peers with the primary first.
func relaySource(c *conn, peers []*conn) bool {
	var bloomPeers = 0
	for _, other := range peers {
		if servesBloom(other) { bloomPeers++ }
	}
	if bloomPeers > 0 {
		return servesBloom(c)
	}
	return len(peers) > 0 && peers[0] == c
}

func servesBloom(c *conn) bool {
	return c.peer != nil && c.peer.Services()&wire.SFNodeBloom != 0
}

// describeRelay names how unconfirmed transactions are received from the
// connected peers, the primary first.
func describeRelay(peers []*conn) string {
	var bloomPeers = 0
	for _, c := range peers {
		if servesBloom(c) { bloomPeers++ }
	}
	switch {
	case bloomPeers > 0:
		return "bloom filters on " + plural(bloomPeers, "peer")
	case len(peers) > 0:
		return "no peer serves bloom filters, downloading all relayed transactions from " + peers[0].addr
	}
	return "no peers"
}

// logRelayMode logs how unconfirmed transactions are received, when that
// changed since the last call.
func logRelayMode() {
	var mode = describeRelay(orderedPeers())
	relayMu.Lock()
	defer relayMu.Unlock()
	if mode == relayMode { return }
	relayMode = mode
	log.Printf("tx relay: %s", mode)
}

func plural(n int, noun string) string {
	if n == 1 { return "1 " + noun }
	return strconv.Itoa(n) + " " + noun + "s"
}
