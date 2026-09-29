package sync

import "bytes"
import "math/rand/v2"
import "path/filepath"
import "testing"
import "time"
import "github.com/btcsuite/btcd/btcutil"
import "github.com/btcsuite/btcd/btcutil/bloom"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/storage"

// testWallet opens a fresh store watching one P2WPKH script.
func testWallet(t *testing.T) []byte {
	t.Helper()
	resetSync()
	var st, err = storage.Open(filepath.Join(t.TempDir(), "w.db"), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	store = st
	var mine = append([]byte{0x00, 0x14}, bytes.Repeat([]byte{7}, 20)...)
	scripts = []watchScript{{address: "bc1mine", script: mine}}
	scriptIndex = map[string]int{string(mine): 0}
	if err := loadWallet(); err != nil {
		t.Fatalf("loadWallet: %v", err)
	}
	return mine
}

// TestComputeBalance checks confirmed coins, pending receives and pending
// spends of confirmed coins.
func TestComputeBalance(t *testing.T) {
	var mine = []byte{0x00, 0x14, 1}
	var isMine = func(s []byte) bool { return bytes.Equal(s, mine) }
	var recv1 = testTx(wire.OutPoint{Hash: chainhash.Hash{1}}, mine, []byte{0x51})
	var recv2 = testTx(wire.OutPoint{Hash: chainhash.Hash{2}}, []byte{0x51}, mine)
	var spend = testTx(wire.OutPoint{Hash: recv1.TxHash(), Index: 0}, []byte{0x52})
	if c, p := computeBalance([]*wire.MsgTx{recv1, recv2}, nil, isMine); c != 3000 || p != 0 {
		t.Fatalf("two receives: %d confirmed, %d pending; want 3000, 0", c, p)
	}
	if c, p := computeBalance([]*wire.MsgTx{recv1}, []*wire.MsgTx{recv2}, isMine); c != 1000 || p != 2000 {
		t.Fatalf("pending receive: %d confirmed, %d pending; want 1000, +2000", c, p)
	}
	if c, p := computeBalance([]*wire.MsgTx{recv1, recv2}, []*wire.MsgTx{spend}, isMine); c != 3000 || p != -1000 {
		t.Fatalf("pending spend: %d confirmed, %d pending; want 3000, -1000", c, p)
	}
	if c, _ := computeBalance([]*wire.MsgTx{recv1, recv2, spend}, nil, isMine); c != 2000 {
		t.Fatalf("confirmed spend: %d confirmed, want 2000", c)
	}
}

// TestPendingLifecycle checks that a relayed transaction paying the wallet
// becomes pending, an unrelated one is ignored, and a block confirming it
// moves it to the confirmed balance.
func TestPendingLifecycle(t *testing.T) {
	var mine = testWallet(t)
	var c = &conn{addr: "10.0.0.1:8333"}
	var incoming = testTx(wire.OutPoint{Hash: chainhash.Hash{1}}, []byte{0x51}, mine)
	acceptPendingTx(c, testTx(wire.OutPoint{Hash: chainhash.Hash{2}}, []byte{0x51}))
	acceptPendingTx(c, incoming)
	acceptPendingTx(c, incoming)
	if conf, pend := walletBalance(); conf != 0 || pend != 2000 {
		t.Fatalf("after relay: %d confirmed, %d pending; want 0, 2000", conf, pend)
	}
	if list, _ := store.PendingTransactions(); len(list) != 1 {
		t.Fatalf("pending rows = %d, want 1", len(list))
	}
	if _, err := processBlock(10, testBlock(incoming)); err != nil {
		t.Fatalf("processBlock: %v", err)
	}
	if conf, pend := walletBalance(); conf != 2000 || pend != 0 {
		t.Fatalf("after confirmation: %d confirmed, %d pending; want 2000, 0", conf, pend)
	}
	if list, _ := store.PendingTransactions(); len(list) != 0 {
		t.Fatalf("confirmed tx still pending: %+v", list)
	}
	acceptPendingTx(c, incoming)
	if _, pend := walletBalance(); pend != 0 {
		t.Fatalf("confirmed tx accepted as pending again: %d", pend)
	}
}

// TestPendingConflict checks that a block spending the same input as a
// pending transaction drops it.
func TestPendingConflict(t *testing.T) {
	var mine = testWallet(t)
	var input = wire.OutPoint{Hash: chainhash.Hash{5}}
	acceptPendingTx(&conn{addr: "p"}, testTx(input, mine))
	if _, pend := walletBalance(); pend != 1000 {
		t.Fatalf("pending = %d, want 1000", pend)
	}
	var replacement = testTx(input, []byte{0x51})
	if _, err := processBlock(11, testBlock(replacement)); err != nil {
		t.Fatalf("processBlock: %v", err)
	}
	if conf, pend := walletBalance(); conf != 0 || pend != 0 {
		t.Fatalf("after conflict: %d confirmed, %d pending; want 0, 0", conf, pend)
	}
}

// TestExpirePending checks that stale unconfirmed transactions are dropped.
func TestExpirePending(t *testing.T) {
	var mine = testWallet(t)
	var tx = testTx(wire.OutPoint{Hash: chainhash.Hash{6}}, mine)
	var buf bytes.Buffer
	_ = tx.Serialize(&buf)
	var old = time.Now().Add(-pendingExpiry - time.Hour).Unix()
	if err := store.SavePending(storage.PendingTx{Txid: tx.TxHash(), Raw: buf.Bytes(), SeenAt: old}); err != nil {
		t.Fatalf("SavePending: %v", err)
	}
	if err := loadWallet(); err != nil {
		t.Fatalf("loadWallet: %v", err)
	}
	if err := expirePending(); err != nil {
		t.Fatalf("expirePending: %v", err)
	}
	if _, pend := walletBalance(); pend != 0 {
		t.Fatalf("expired tx still pending: %d", pend)
	}
}

// TestWalletBloom checks that the filter matches a payment to the wallet and
// a spend of a wallet coin, but not an unrelated transaction.
func TestWalletBloom(t *testing.T) {
	var mine = testWallet(t)
	var coin = wire.OutPoint{Hash: chainhash.Hash{8}, Index: 1}
	outpoints[coin] = "bc1mine"
	var filter = bloom.LoadFilter(walletBloom())
	if !filter.MatchTxAndUpdate(btcutil.NewTx(testTx(wire.OutPoint{Hash: chainhash.Hash{9}}, mine))) {
		t.Error("payment to the wallet not matched")
	}
	if !filter.MatchTxAndUpdate(btcutil.NewTx(testTx(coin, []byte{0x51}))) {
		t.Error("spend of a wallet coin not matched")
	}
	var other = append([]byte{0x00, 0x14}, bytes.Repeat([]byte{3}, 20)...)
	if filter.MatchTxAndUpdate(btcutil.NewTx(testTx(wire.OutPoint{Hash: chainhash.Hash{10}}, other))) {
		t.Error("unrelated transaction matched")
	}
}

// TestWalletBloomStaysSelective checks that the filter of a one-address
// wallet keeps rejecting unrelated transactions: matches must not make it
// grow until it matches everything.
func TestWalletBloomStaysSelective(t *testing.T) {
	testWallet(t)
	var filter = bloom.LoadFilter(walletBloom())
	var rng = rand.New(rand.NewPCG(1, 2))
	var matched = 0
	for range 5000 {
		var prev wire.OutPoint
		for i := range prev.Hash {
			prev.Hash[i] = byte(rng.Uint32())
		}
		var outs [][]byte
		for range 3 {
			var script = []byte{0x00, 0x14}
			for range 20 {
				script = append(script, byte(rng.Uint32()))
			}
			outs = append(outs, script)
		}
		if filter.MatchTxAndUpdate(btcutil.NewTx(testTx(prev, outs...))) {
			matched++
		}
	}
	if matched > 25 {
		t.Fatalf("%d of 5000 unrelated transactions matched, the filter is not selective", matched)
	}
}

// TestUsedAndWatch checks that pending and confirmed payments mark their
// address used, and that a watched new address is matched from then on.
func TestUsedAndWatch(t *testing.T) {
	var mine = testWallet(t)
	if IsUsed("bc1mine") {
		t.Fatal("fresh address reported used")
	}
	acceptPendingTx(&conn{addr: "p"}, testTx(wire.OutPoint{Hash: chainhash.Hash{1}}, mine))
	if !IsUsed("bc1mine") {
		t.Fatal("address with a pending payment not reported used")
	}
	var pubkey = bytes.Repeat([]byte{2}, 33)
	Watch("bc1next", pubkey)
	Watch("bc1next", pubkey)
	if len(watchedScripts()) != 2 {
		t.Fatalf("watched scripts = %d, want 2", len(watchedScripts()))
	}
	if IsUsed("bc1next") {
		t.Fatal("new address reported used")
	}
	var next = p2wpkhScript(pubkey)
	if _, err := processBlock(12, testBlock(testTx(wire.OutPoint{Hash: chainhash.Hash{2}}, next))); err != nil {
		t.Fatalf("processBlock: %v", err)
	}
	if !IsUsed("bc1next") {
		t.Fatal("address with a confirmed payment not reported used")
	}
	if conf, pend := walletBalance(); conf != 1000 || pend != 1000 {
		t.Fatalf("balance = %d confirmed, %d pending; want 1000, 1000", conf, pend)
	}
}

// TestWatchBeforeInit checks that watching an address before the sync is
// initialised is a harmless no-op.
func TestWatchBeforeInit(t *testing.T) {
	resetSync()
	scriptIndex = nil
	Watch("bc1early", bytes.Repeat([]byte{2}, 33))
	if len(scripts) != 0 {
		t.Fatalf("address watched before init: %v", scripts)
	}
}
