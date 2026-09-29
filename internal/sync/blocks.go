package sync

import "bytes"
import "fmt"
import "log"
import "github.com/btcsuite/btcd/blockchain"
import "github.com/btcsuite/btcd/btcutil"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/storage"

// fetchBlock downloads the block at the height from the connected peers in
// turn, the primary first, and returns the first copy that verifies against the header chain. A
// peer serving a block that fails verification is disconnected.
func fetchBlock(height int32) (*wire.MsgBlock, error) {
	var hdr, ok = chain.HeaderAt(height)
	if !ok {
		return nil, fmt.Errorf("no header at height %d", height)
	}
	var peers = orderedPeers()
	if len(peers) == 0 {
		return nil, fmt.Errorf("no connected peers")
	}
	var lastErr error
	for _, c := range peers {
		var block, err = c.getBlock(hdr.Hash)
		if err != nil {
			log.Printf("peer %s: block %d: %v", c.addr, height, err)
			lastErr = err
			continue
		}
		if err := verifyBlock(block, hdr.Hash); err != nil {
			c.drop(fmt.Sprintf("block %d", height), err)
			lastErr = err
			continue
		}
		log.Printf("sync: downloaded block %d (%s, %d transactions) from %s", height, hdr.Hash, len(block.Transactions), c.addr)
		return block, nil
	}
	return nil, fmt.Errorf("no peer served block %s: %w", hdr.Hash, lastErr)
}

// verifyBlock checks that the block is the one the header chain committed
// to: its header hashes to the expected block hash, its transactions hash to
// the header's merkle root without duplicates, and its witness data matches
// the coinbase witness commitment.
func verifyBlock(block *wire.MsgBlock, want chainhash.Hash) error {
	if got := block.BlockHash(); got != want {
		return fmt.Errorf("block hash %s, want %s", got, want)
	}
	if len(block.Transactions) == 0 {
		return fmt.Errorf("block has no transactions")
	}
	var txs = make([]*btcutil.Tx, len(block.Transactions))
	var seen = make(map[chainhash.Hash]bool, len(block.Transactions))
	for i, tx := range block.Transactions {
		txs[i] = btcutil.NewTx(tx)
		var id = tx.TxHash()
		if seen[id] {
			return fmt.Errorf("duplicate transaction %s", id)
		}
		seen[id] = true
	}
	if root := blockchain.CalcMerkleRoot(txs, false); root != block.Header.MerkleRoot {
		return fmt.Errorf("merkle root %s, header commits to %s", root, block.Header.MerkleRoot)
	}
	if err := blockchain.ValidateWitnessCommitment(btcutil.NewBlock(block)); err != nil {
		return fmt.Errorf("witness commitment: %w", err)
	}
	return nil
}

// processBlock stores every transaction of the block that pays to a wallet
// script or spends a wallet coin, and tracks the wallet coins it creates. It
// returns the number of wallet transactions found.
func processBlock(height int32, block *wire.MsgBlock) (int, error) {
	var blockHash = block.BlockHash()
	var found = 0
	for _, tx := range block.Transactions {
		var txid = tx.TxHash()
		var relevant = false
		for _, in := range tx.TxIn {
			if addr, ok := outpoints[in.PreviousOutPoint]; ok {
				relevant = true
				log.Printf("match: tx %s in block %d spends %s of %s", txid, height, in.PreviousOutPoint, addr)
			}
		}
		for i, out := range tx.TxOut {
			var idx, ok = scriptIndex[string(out.PkScript)]
			if !ok { continue }
			relevant = true
			var addr = scripts[idx].address
			outpoints[wire.OutPoint{Hash: txid, Index: uint32(i)}] = addr
			log.Printf("match: tx %s in block %d pays %d sat to %s", txid, height, out.Value, addr)
		}
		if !relevant { continue }
		var buf bytes.Buffer
		if err := tx.Serialize(&buf); err != nil {
			return found, fmt.Errorf("serialize tx %s: %w", txid, err)
		}
		if err := store.SaveTransaction(storage.Transaction{Txid: txid, Height: height, BlockHash: blockHash, Raw: buf.Bytes()}); err != nil {
			return found, fmt.Errorf("store tx %s: %w", txid, err)
		}
		found++
	}
	return found, nil
}

// loadOutpoints rebuilds the set of wallet coins from the stored wallet
// transactions, so spends of them are recognised.
func loadOutpoints() error {
	outpoints = make(map[wire.OutPoint]string)
	var txs, err = store.Transactions()
	if err != nil {
		return fmt.Errorf("load transactions: %w", err)
	}
	for _, t := range txs {
		var tx wire.MsgTx
		if err := tx.Deserialize(bytes.NewReader(t.Raw)); err != nil {
			return fmt.Errorf("decode stored tx %s: %w", t.Txid, err)
		}
		for i, out := range tx.TxOut {
			if idx, ok := scriptIndex[string(out.PkScript)]; ok {
				outpoints[wire.OutPoint{Hash: t.Txid, Index: uint32(i)}] = scripts[idx].address
			}
		}
	}
	return nil
}
