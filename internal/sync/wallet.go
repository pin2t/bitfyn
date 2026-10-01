package sync

import "bytes"
import "fmt"
import "log"
import "sort"
import "sync"
import "time"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/peer"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/storage"
import "bitfyn/internal/wallet"

// pendingExpiry drops unconfirmed transactions that never confirmed. It is
// the default mempool expiry of Bitcoin Core.
const pendingExpiry = 14 * 24 * time.Hour

// seenLimit bounds the set of relayed transaction ids already requested.
const seenLimit = 10000

// The wallet state is rebuilt from the stored confirmed and unconfirmed
// wallet transactions. walletMu guards it: the sync loop confirms
// transactions while peer goroutines deliver unconfirmed ones.
var walletMu sync.Mutex
var outpoints map[wire.OutPoint]string
var confirmedIDs map[chainhash.Hash]bool
var pendingIDs map[chainhash.Hash]bool
var pendingSpends map[wire.OutPoint]chainhash.Hash
var seenTxs = make(map[chainhash.Hash]bool)
var usedAddrs map[string]bool
var firstPaid map[string]int32
var coins []wallet.Coin
var pendingTxs []*wire.MsgTx
var confirmedBalance int64
var pendingBalance int64

// loadWallet rebuilds the wallet state from the database.
func loadWallet() error {
	walletMu.Lock()
	defer walletMu.Unlock()
	return loadWalletLocked()
}

// loadWalletLocked rebuilds the wallet coins, the inputs spent by pending
// transactions and the balances. walletMu must be held.
func loadWalletLocked() error {
	var stored, err = store.Transactions()
	if err != nil {
		return fmt.Errorf("load transactions: %w", err)
	}
	pending, err := store.PendingTransactions()
	if err != nil {
		return fmt.Errorf("load pending transactions: %w", err)
	}
	var confirmed = make([]*wire.MsgTx, 0, len(stored))
	var heights = make([]int32, 0, len(stored))
	var times = make([]int64, 0, len(stored)+len(pending))
	confirmedIDs = make(map[chainhash.Hash]bool, len(stored))
	for _, t := range stored {
		var tx, err = decodeTx(t.Raw)
		if err != nil {
			return fmt.Errorf("decode stored tx %s: %w", t.Txid, err)
		}
		confirmed = append(confirmed, tx)
		heights = append(heights, t.Height)
		times = append(times, blockTime(t.Height))
		confirmedIDs[t.Txid] = true
	}
	var unconfirmed = make([]*wire.MsgTx, 0, len(pending))
	pendingIDs = make(map[chainhash.Hash]bool, len(pending))
	pendingSpends = make(map[wire.OutPoint]chainhash.Hash)
	for _, p := range pending {
		var tx, err = decodeTx(p.Raw)
		if err != nil {
			return fmt.Errorf("decode pending tx %s: %w", p.Txid, err)
		}
		unconfirmed = append(unconfirmed, tx)
		times = append(times, p.SeenAt)
		pendingIDs[p.Txid] = true
		for _, in := range tx.TxIn {
			pendingSpends[in.PreviousOutPoint] = p.Txid
		}
	}
	outpoints = make(map[wire.OutPoint]string)
	usedAddrs = make(map[string]bool)
	firstPaid = make(map[string]int32)
	var unspent = make(map[wire.OutPoint]wallet.Coin)
	for n, tx := range append(confirmed, unconfirmed...) {
		var txid = tx.TxHash()
		for i, out := range tx.TxOut {
			var idx, ok = scriptIndex[string(out.PkScript)]
			if !ok { continue }
			var w = scripts[idx]
			var op = wire.OutPoint{Hash: txid, Index: uint32(i)}
			outpoints[op] = w.address
			usedAddrs[w.address] = true
			if n < len(confirmed) {
				if h, ok := firstPaid[w.address]; !ok || heights[n] < h {
					firstPaid[w.address] = heights[n]
				}
			}
			unspent[op] = wallet.Coin{OutPoint: op, Value: out.Value, PkScript: out.PkScript, Address: w.address, Path: w.path, Confirmed: n < len(confirmed), Time: times[n]}
		}
	}
	for _, tx := range append(confirmed, unconfirmed...) {
		for _, in := range tx.TxIn {
			delete(unspent, in.PreviousOutPoint)
		}
	}
	coins = make([]wallet.Coin, 0, len(unspent))
	for _, c := range unspent {
		coins = append(coins, c)
	}
	sort.Slice(coins, func(i, j int) bool {
		if coins[i].Value != coins[j].Value { return coins[i].Value > coins[j].Value }
		return coins[i].OutPoint.String() < coins[j].OutPoint.String()
	})
	pendingTxs = unconfirmed
	confirmedBalance, pendingBalance = computeBalance(confirmed, unconfirmed, isWalletScript)
	return nil
}

// blockTime is the unix time of the block at the height, or 0 when its
// header is not loaded.
func blockTime(height int32) int64 {
	if chain == nil { return 0 }
	var h, ok = chain.HeaderAt(height)
	if !ok { return 0 }
	return h.Timestamp.Unix()
}

func decodeTx(raw []byte) (*wire.MsgTx, error) {
	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return &tx, nil
}

func isWalletScript(script []byte) bool {
	var _, ok = scriptIndex[string(script)]
	return ok
}

// computeBalance returns the confirmed balance, the value of the wallet
// coins left unspent by the confirmed transactions, and the pending change
// the unconfirmed transactions make to it, in satoshis.
func computeBalance(confirmed, unconfirmed []*wire.MsgTx, isMine func([]byte) bool) (int64, int64) {
	var settled = unspentValue(confirmed, isMine)
	var all = make([]*wire.MsgTx, 0, len(confirmed)+len(unconfirmed))
	all = append(all, confirmed...)
	all = append(all, unconfirmed...)
	return settled, unspentValue(all, isMine) - settled
}

// unspentValue sums the wallet outputs of the transactions that none of
// them spends.
func unspentValue(txs []*wire.MsgTx, isMine func([]byte) bool) int64 {
	var coins = make(map[wire.OutPoint]int64)
	for _, tx := range txs {
		var txid = tx.TxHash()
		for i, out := range tx.TxOut {
			if isMine(out.PkScript) {
				coins[wire.OutPoint{Hash: txid, Index: uint32(i)}] = out.Value
			}
		}
	}
	for _, tx := range txs {
		for _, in := range tx.TxIn {
			delete(coins, in.PreviousOutPoint)
		}
	}
	var sum = int64(0)
	for _, value := range coins {
		sum += value
	}
	return sum
}

// IsUsed reports whether the address has received a payment, confirmed or
// still pending.
func IsUsed(address string) bool {
	walletMu.Lock()
	defer walletMu.Unlock()
	return usedAddrs[address]
}

// Coins returns the spendable wallet coins, confirmed and unconfirmed, the
// largest first.
func Coins() []wallet.Coin {
	walletMu.Lock()
	defer walletMu.Unlock()
	return append([]wallet.Coin(nil), coins...)
}

// Watch adds a newly derived P2WPKH wallet address, receive or change, with
// the derivation path of its key to the watched scripts, so filters, blocks
// and relayed transactions are matched against it too, and refreshes the
// peers' bloom filters. The wallet is rebuilt: stored transactions may pay
// the address already, when another wallet on the same seed used it first.
// Their coins appear at once, and a rescan from the first block paying it is
// queued, since their spends were not looked for in the blocks scanned
// before. It does nothing before the sync is initialised: Init loads every
// stored address.
func Watch(address, path string, pubkey []byte) {
	var script = p2wpkhScript(pubkey)
	walletMu.Lock()
	if scriptIndex == nil {
		walletMu.Unlock()
		return
	}
	if _, ok := scriptIndex[string(script)]; ok {
		walletMu.Unlock()
		return
	}
	scriptIndex[string(script)] = len(scripts)
	scripts = append(scripts, watchScript{address: address, path: path, script: script})
	var err = loadWalletLocked()
	var first, paid = firstPaid[address]
	if err == nil && paid {
		err = store.AddRescan(storage.Rescan{Address: address, From: first})
	}
	walletMu.Unlock()
	log.Printf("sync: watching address %s", address)
	if err != nil {
		log.Printf("sync: watch address %s: %v", address, err)
	}
	if !paid {
		reloadBlooms()
		return
	}
	log.Printf("sync: address %s was paid in block %d before it was watched, rescan queued", address, first)
	walletChanged()
	wakeSync()
}

// watchedScripts returns a snapshot of the watched wallet scripts.
func watchedScripts() []watchScript {
	walletMu.Lock()
	defer walletMu.Unlock()
	return append([]watchScript(nil), scripts...)
}

// walletBalance returns the confirmed balance and the pending change.
func walletBalance() (int64, int64) {
	walletMu.Lock()
	defer walletMu.Unlock()
	return confirmedBalance, pendingBalance
}

// describeTx logs how a wallet transaction touches the wallet and reports
// whether it does. walletMu must be held.
func describeTx(tx *wire.MsgTx, where string) bool {
	var txid = tx.TxHash()
	var relevant = false
	for _, in := range tx.TxIn {
		if addr, ok := outpoints[in.PreviousOutPoint]; ok {
			relevant = true
			log.Printf("match: tx %s %s spends %s of %s", txid, where, in.PreviousOutPoint, addr)
		}
	}
	for _, out := range tx.TxOut {
		var idx, ok = scriptIndex[string(out.PkScript)]
		if !ok { continue }
		relevant = true
		log.Printf("match: tx %s %s pays %d sat to %s", txid, where, out.Value, scripts[idx].address)
	}
	return relevant
}

// processBlock stores every transaction of the verified block that pays to
// a wallet script or spends a wallet coin. A pending transaction it confirms
// leaves the pending set, and a pending transaction conflicting with one of
// its transactions is dropped. It returns the number of wallet transactions
// found.
func processBlock(height int32, block *wire.MsgBlock) (int, error) {
	var blockHash = block.BlockHash()
	var found = 0
	var changed = false
	walletMu.Lock()
	var where = fmt.Sprintf("in block %d", height)
	for _, tx := range block.Transactions {
		var txid = tx.TxHash()
		for _, in := range tx.TxIn {
			var other, ok = pendingSpends[in.PreviousOutPoint]
			if !ok || other == txid { continue }
			if err := store.DeletePending(other); err != nil {
				walletMu.Unlock()
				return found, fmt.Errorf("drop pending tx %s: %w", other, err)
			}
			delete(pendingSpends, in.PreviousOutPoint)
			changed = true
			log.Printf("pending: tx %s dropped, it conflicts with tx %s in block %d", other, txid, height)
		}
		if !describeTx(tx, where) { continue }
		var buf bytes.Buffer
		if err := tx.Serialize(&buf); err != nil {
			walletMu.Unlock()
			return found, fmt.Errorf("serialize tx %s: %w", txid, err)
		}
		if err := store.SaveTransaction(storage.Transaction{Txid: txid, Height: height, BlockHash: blockHash, Raw: buf.Bytes()}); err != nil {
			walletMu.Unlock()
			return found, fmt.Errorf("store tx %s: %w", txid, err)
		}
		if pendingIDs[txid] {
			if err := store.DeletePending(txid); err != nil {
				walletMu.Unlock()
				return found, fmt.Errorf("confirm pending tx %s: %w", txid, err)
			}
			log.Printf("pending: tx %s confirmed in block %d", txid, height)
		}
		found++
		changed = true
	}
	var err error
	if changed {
		err = loadWalletLocked()
	}
	walletMu.Unlock()
	if changed {
		walletChanged()
	}
	return found, err
}

// requestTxs asks the peer for the announced transactions not seen before,
// without witness data: the inputs and outputs are all the wallet checks.
// It takes the listener's peer: an announcement may arrive during the
// handshake, before the conn holds it.
func requestTxs(p *peer.Peer, msg *wire.MsgInv) {
	var kind = wire.InvTypeTx
	var getData = wire.NewMsgGetData()
	walletMu.Lock()
	for _, iv := range msg.InvList {
		if iv.Type != wire.InvTypeTx && iv.Type != wire.InvTypeWitnessTx { continue }
		if seenTxs[iv.Hash] || confirmedIDs[iv.Hash] || pendingIDs[iv.Hash] { continue }
		if len(seenTxs) >= seenLimit {
			seenTxs = make(map[chainhash.Hash]bool)
		}
		seenTxs[iv.Hash] = true
		_ = getData.AddInvVect(wire.NewInvVect(kind, &iv.Hash))
	}
	walletMu.Unlock()
	if len(getData.InvList) > 0 {
		p.QueueMessage(getData, nil)
	}
}

// acceptPendingTx stores a relayed unconfirmed transaction when it pays to a
// wallet script or spends a wallet coin. Bloom filter false positives are
// ignored.
func acceptPendingTx(c *conn, tx *wire.MsgTx) {
	var txid = tx.TxHash()
	walletMu.Lock()
	if confirmedIDs[txid] || pendingIDs[txid] || !describeTx(tx, "unconfirmed, from "+c.addr+",") {
		walletMu.Unlock()
		return
	}
	var buf bytes.Buffer
	var err = tx.Serialize(&buf)
	if err == nil {
		err = store.SavePending(storage.PendingTx{Txid: txid, Raw: buf.Bytes(), SeenAt: time.Now().Unix()})
	}
	if err == nil {
		err = loadWalletLocked()
	}
	walletMu.Unlock()
	if err != nil {
		log.Printf("pending: store tx %s: %v", txid, err)
		return
	}
	walletChanged()
}

// expirePending drops the unconfirmed transactions older than pendingExpiry.
func expirePending() error {
	var n, err = store.DeletePendingBefore(time.Now().Add(-pendingExpiry).Unix())
	if err != nil {
		return fmt.Errorf("expire pending transactions: %w", err)
	}
	if n == 0 {
		return nil
	}
	log.Printf("pending: %d unconfirmed transactions expired after %s", n, pendingExpiry)
	if err := loadWallet(); err != nil {
		return err
	}
	walletChanged()
	return nil
}

// walletChanged reports the new balance and refreshes the bloom filters of
// the connected peers with the current wallet coins.
func walletChanged() {
	var confirmed, pending = walletBalance()
	log.Printf("balance: %d sat confirmed, %+d sat pending", confirmed, pending)
	reloadBlooms()
	emit()
}

// Broadcast sends a signed wallet transaction to every connected peer and
// records it as pending, so the balance reflects it at once. It is sent
// again to every peer that connects until it confirms. It returns the
// number of peers it was sent to. Transactions go out with witness encoding:
// the plain QueueMessage would strip the signatures of segwit inputs.
func Broadcast(tx *wire.MsgTx) (int, error) {
	var peers = livePeers(poolPeers())
	if len(peers) == 0 {
		return 0, fmt.Errorf("not connected to any peer")
	}
	var txid = tx.TxHash()
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return 0, fmt.Errorf("serialize tx %s: %w", txid, err)
	}
	walletMu.Lock()
	var err = store.SavePending(storage.PendingTx{Txid: txid, Raw: buf.Bytes(), SeenAt: time.Now().Unix()})
	if err == nil {
		seenTxs[txid] = true
		describeTx(tx, "sent,")
		err = loadWalletLocked()
	}
	walletMu.Unlock()
	if err != nil {
		return 0, fmt.Errorf("record tx %s: %w", txid, err)
	}
	for _, c := range peers {
		c.peer.QueueMessageWithEncoding(tx, nil, wire.WitnessEncoding)
	}
	log.Printf("send: tx %s broadcast to %d peers", txid, len(peers))
	walletChanged()
	return len(peers), nil
}

// rebroadcast sends the pending transactions spending wallet coins, the
// wallet's own unconfirmed payments, to a newly connected peer.
func rebroadcast(c *conn) {
	walletMu.Lock()
	var own []*wire.MsgTx
	for _, tx := range pendingTxs {
		for _, in := range tx.TxIn {
			if _, ok := outpoints[in.PreviousOutPoint]; ok {
				own = append(own, tx)
				break
			}
		}
	}
	walletMu.Unlock()
	for _, tx := range own {
		c.peer.QueueMessageWithEncoding(tx, nil, wire.WitnessEncoding)
	}
	if len(own) > 0 {
		log.Printf("send: %d pending transactions sent again to %s", len(own), c.addr)
	}
}
