package sync

import "log"
import "math/rand/v2"
import "github.com/btcsuite/btcd/btcutil/bloom"
import "github.com/btcsuite/btcd/wire"

// bloomFPRate is the false positive rate of the BIP37 wallet filter. Lower
// rates leak more about which addresses belong to the wallet; higher rates
// make peers relay more unrelated transactions. bloomMinElements sizes the
// filter for at least that many elements: a filter sized for one or two
// elements is only a few bytes and far above its nominal rate.
const bloomFPRate = 0.0001
const bloomMinElements = 20

// walletBloom builds the BIP37 filter of the wallet: the public key hash of
// every wallet address, matched by peers in the output scripts, and every
// wallet coin, matched in the inputs spending it. The peer must not add to
// the filter itself (BloomUpdateNone): every false positive would add the
// outputs of an unrelated transaction until the filter matches everything.
// The wallet reloads the filter instead whenever its coins change.
func walletBloom() *wire.MsgFilterLoad {
	walletMu.Lock()
	defer walletMu.Unlock()
	var elements = uint32(len(scripts) + len(outpoints))
	var filter = bloom.NewFilter(max(elements, bloomMinElements), rand.Uint32(), bloomFPRate, wire.BloomUpdateNone)
	for _, w := range scripts {
		filter.Add(w.script[2:])
	}
	for op := range outpoints {
		filter.AddOutPoint(&op)
	}
	return filter.MsgFilterLoad()
}

// loadBloom sends the wallet filter to a peer serving BIP37 filters and asks
// it for the matching transactions already in its mempool. From then on the
// peer announces every matching transaction it relays. It reports whether
// the peer took a filter.
func (c *conn) loadBloom() bool {
	if c.peer.Services()&wire.SFNodeBloom == 0 {
		return false
	}
	var msg = walletBloom()
	c.peer.QueueMessage(msg, nil)
	c.peer.QueueMessage(wire.NewMsgMemPool(), nil)
	log.Printf("peer %s: bloom filter loaded (%d bytes, %d hash functions)", c.addr, len(msg.Filter), msg.HashFuncs)
	return true
}

// reloadBlooms refreshes the filter of every connected peer serving BIP37,
// after the wallet coins changed.
func reloadBlooms() {
	for _, c := range livePeers(poolPeers()) {
		if c.peer == nil || c.peer.Services()&wire.SFNodeBloom == 0 { continue }
		c.peer.QueueMessage(walletBloom(), nil)
	}
}
