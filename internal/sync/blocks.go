package sync

import "fmt"
import "log"
import "github.com/btcsuite/btcd/blockchain"
import "github.com/btcsuite/btcd/btcutil"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"

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
