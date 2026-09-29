package sync

import "fmt"
import "log"
import "strings"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/txscript"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/spv"
import "bitfyn/internal/storage"

// variant is one version of a block filter together with the peers that
// served it.
type variant struct {
	data  []byte
	hash  chainhash.Hash
	peers []*conn
}

// syncFilters downloads and verifies the BIP158 basic filters starting from
// the block at which the wallet seed was created, matching each filter
// against the wallet scripts. Filter hashes and filters are downloaded from
// up to TargetPeers peers at once and compared per block, following the
// BIP157 client guidance: a filter every peer agrees on is accepted, while a
// disagreement is settled against the full block. Every stored filter is
// pruned right after matching; only its chained header is kept, and each
// batch of rows is written in one transaction. It returns the number of new
// filters stored.
func syncFilters() (int32, error) {
	RefreshFilterStart()
	if err := store.PruneFilterDataFrom(filterStart); err != nil {
		return 0, fmt.Errorf("prune stored filter data: %w", err)
	}
	var start, err = store.FilterResumeHeight(filterStart)
	if err != nil {
		return 0, fmt.Errorf("filter resume height: %w", err)
	}
	var tip = chain.Height()
	var stored = int32(0)
	if start <= tip {
		report(StateFilters, start)
	}
	for start <= tip {
		var end = min(start+filterHeaderBatch-1, tip)
		var peers, hashes, err = filterHashes(start, end)
		if err != nil {
			return stored, fmt.Errorf("filter headers %d-%d: %w", start, end, err)
		}
		for fStart := start; fStart <= end; fStart += filterBatch {
			var fEnd = min(fStart+filterBatch-1, end)
			var n, err = syncFilterBatch(peers, hashes, start, fStart, fEnd)
			stored += n
			if err != nil {
				return stored, fmt.Errorf("filters %d-%d: %w", fStart, fEnd, err)
			}
			peers = livePeers(peers)
			report(StateFilters, fEnd)
		}
		start = end + 1
	}
	return stored, nil
}

// filterHashes fetches the cfheaders batch start..end from the filter peers
// and keeps the peers whose batch links to the stored filter header chain. It
// returns those peers with their filter hashes, indexed by height - start.
func filterHashes(start, end int32) ([]*conn, [][]chainhash.Hash, error) {
	var peers = filterPeers()
	if len(peers) == 0 {
		return nil, nil, fmt.Errorf("no connected peers")
	}
	var stopHdr, _ = chain.HeaderAt(end)
	var resps []*wire.MsgCFHeaders
	peers, resps = fanOut(peers, "cfheaders", func(c *conn) (*wire.MsgCFHeaders, error) {
		return c.getCFHeaders(start, stopHdr.Hash, requestTimeout)
	})
	if len(peers) == 0 {
		return nil, nil, fmt.Errorf("every peer failed the cfheaders request")
	}
	if start == filterStart && filterStart > 0 && !anchorSet {
		var votes = make([]chainhash.Hash, len(resps))
		for i, r := range resps {
			votes[i] = r.PrevFilterHeader
		}
		var anchor, err = MajorityAnchor(votes)
		if err != nil {
			anchor = votes[0]
			log.Printf("sync: filter peers disagree on the filter header anchor (%v); trusting %s", err, peers[0].addr)
		}
		SetFilterAnchor(anchor)
	}
	var okPeers []*conn
	var hashes [][]chainhash.Hash
	for i, c := range peers {
		if err := checkFilterPrev(start, resps[i].PrevFilterHeader); err != nil {
			c.drop("cfheaders", err)
			continue
		}
		var list = make([]chainhash.Hash, len(resps[i].FilterHashes))
		for j, h := range resps[i].FilterHashes {
			list[j] = *h
		}
		okPeers = append(okPeers, c)
		hashes = append(hashes, list)
	}
	if len(okPeers) == 0 {
		return nil, nil, fmt.Errorf("no peer served a filter header chain linking to height %d", start-1)
	}
	if err := requirePrimary(okPeers); err != nil {
		return nil, nil, err
	}
	return okPeers, hashes, nil
}

// syncFilterBatch downloads the filters fStart..fEnd from every peer, checks
// each against the filter hashes that peer committed to, then accepts the
// filters block by block. hashes is indexed by height - base. It returns the
// number of filters stored.
func syncFilterBatch(peers []*conn, hashes [][]chainhash.Hash, base, fStart, fEnd int32) (int32, error) {
	var byPeer = make(map[*conn][]chainhash.Hash, len(peers))
	for i, c := range peers {
		byPeer[c] = hashes[i]
	}
	var stopHdr, _ = chain.HeaderAt(fEnd)
	var got [][][]byte
	peers, got = fanOut(livePeers(peers), "cfilters", func(c *conn) ([][]byte, error) {
		var data, err = c.getCFilters(fStart, stopHdr.Hash)
		if err != nil { return nil, err }
		for i, d := range data {
			var want = byPeer[c][fStart-base+int32(i)]
			if raw := spv.FilterHash(d); raw != want {
				return nil, fmt.Errorf("filter at height %d hashes to %s, peer committed to %s", fStart+int32(i), raw, want)
			}
		}
		return data, nil
	})
	if len(peers) == 0 {
		return 0, fmt.Errorf("every peer failed the cfilters request")
	}
	if err := requirePrimary(peers); err != nil {
		return 0, err
	}
	if len(peers) == 1 && fStart == base {
		log.Printf("sync: filters %d-%d from %s alone, no second peer to cross-check", fStart, fEnd, peers[0].addr)
	}
	var mismatches = 0
	var n, err = storeFilters(fStart, fEnd-fStart+1, func(i int32) ([]byte, *wire.MsgBlock, error) {
		var variants = groupVariants(peers, got, int(i))
		if len(variants) > 1 { mismatches++ }
		return chooseFilter(fStart+i, variants)
	})
	if err != nil {
		return n, err
	}
	if len(peers) > 1 {
		log.Printf("sync: filters %d-%d cross-checked with %d peers, %d mismatches", fStart, fEnd, len(peers), mismatches)
	}
	return fEnd - fStart + 1, nil
}

// groupVariants groups the filters the peers served for one block by their
// hash, in the order the peers are listed.
func groupVariants(peers []*conn, got [][][]byte, index int) []*variant {
	var variants []*variant
	var byHash = make(map[chainhash.Hash]*variant)
	for i, c := range peers {
		var data = got[i][index]
		var hash = spv.FilterHash(data)
		var v = byHash[hash]
		if v == nil {
			v = &variant{data: data, hash: hash}
			byHash[hash] = v
			variants = append(variants, v)
		}
		v.peers = append(v.peers, c)
	}
	return variants
}

// chooseFilter picks the filter of the block at the height. When the peers
// disagree, the full block is downloaded and verified, every
// variant is checked to contain all of the block's output scripts, and the
// peers that served an invalid or outvoted filter are disconnected. The
// verified block is returned too when it had to be downloaded.
func chooseFilter(height int32, variants []*variant) ([]byte, *wire.MsgBlock, error) {
	var chosen = variants[0]
	var block *wire.MsgBlock
	if len(variants) > 1 {
		var hdr, _ = chain.HeaderAt(height)
		log.Printf("sync: filter mismatch at height %d (block %s): %s", height, hdr.Hash, describeVariants(variants))
		var err error
		block, err = fetchBlock(height)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve filter mismatch at height %d: %w", height, err)
		}
		var winner, valid, rerr = resolveVariants(variants, block)
		if rerr != nil {
			for _, v := range variants {
				for _, c := range v.peers {
					c.drop(fmt.Sprintf("filter at height %d", height), rerr)
				}
			}
			return nil, nil, fmt.Errorf("height %d: %w", height, rerr)
		}
		chosen = variants[winner]
		log.Printf("sync: filter mismatch at height %d resolved against the full block: using %s from %s",
			height, chosen.hash, peerList(chosen.peers))
		for i, v := range variants {
			if i == winner { continue }
			var reason = fmt.Errorf("filter %s is missing output scripts of block %s", v.hash, hdr.Hash)
			if valid[i] {
				reason = fmt.Errorf("filter %s outvoted by %s", v.hash, chosen.hash)
			}
			for _, c := range v.peers {
				c.drop(fmt.Sprintf("filter at height %d", height), reason)
			}
		}
	}
	return chosen.data, block, nil
}

// resolveVariants checks every filter variant against the verified block. A
// valid filter contains every output script of the block; the winner is the
// valid variant served by the most peers, the earliest listed on a tie. valid
// reports the check result per variant.
func resolveVariants(variants []*variant, block *wire.MsgBlock) (int, []bool, error) {
	var blockHash = block.BlockHash()
	var winner = -1
	var valid = make([]bool, len(variants))
	for i, v := range variants {
		var ok, err = filterHasOutputs(v.data, &blockHash, block)
		if err != nil {
			log.Printf("sync: filter %s from %s cannot be decoded: %v", v.hash, peerList(v.peers), err)
			continue
		}
		valid[i] = ok
		if ok && (winner < 0 || len(v.peers) > len(variants[winner].peers)) {
			winner = i
		}
	}
	if winner < 0 {
		return 0, valid, fmt.Errorf("no peer served a filter containing all output scripts of block %s", blockHash)
	}
	return winner, valid, nil
}

// filterHasOutputs reports whether the filter contains every output script
// BIP158 requires: all of them except empty and OP_RETURN scripts. The
// scripts spent by the block's inputs cannot be checked without their
// previous transactions.
func filterHasOutputs(data []byte, blockHash *chainhash.Hash, block *wire.MsgBlock) (bool, error) {
	var seen = make(map[string]bool)
	var outputs [][]byte
	for _, tx := range block.Transactions {
		for _, out := range tx.TxOut {
			var script = out.PkScript
			if len(script) == 0 || script[0] == txscript.OP_RETURN || seen[string(script)] { continue }
			seen[string(script)] = true
			outputs = append(outputs, script)
		}
	}
	var hits, err = spv.MatchScripts(data, blockHash, outputs)
	if err != nil { return false, err }
	for _, hit := range hits {
		if !hit { return false, nil }
	}
	return true, nil
}

// storeFilters accepts count consecutive filters from fStart, as picked one
// by one, and stores their rows in one transaction. The chained filter
// headers are linked in memory from the stored predecessor of fStart. Rows
// accepted before an error are still stored. It returns the number stored.
func storeFilters(fStart, count int32, pick func(i int32) ([]byte, *wire.MsgBlock, error)) (int32, error) {
	var prev, err = prevFilterHeader(fStart)
	if err != nil { return 0, err }
	var rows = make([]storage.Filter, 0, count)
	var failed error
	for i := range count {
		var data, block, err = pick(i)
		if err != nil {
			failed = err
			break
		}
		row, err := applyFilter(fStart+i, data, block, prev)
		if err != nil {
			failed = err
			break
		}
		rows = append(rows, row)
		prev = row.FilterHeader
	}
	if len(rows) > 0 {
		if err := store.SaveFilters(rows); err != nil {
			return 0, fmt.Errorf("store filters %d-%d: %w", fStart, fStart+int32(len(rows))-1, err)
		}
	}
	return int32(len(rows)), failed
}

// applyFilter matches the accepted filter of the block at the height against
// the wallet scripts and returns its row, chained to prev. On a match the
// block is downloaded, when it is not at hand yet, and the wallet
// transactions in it are stored. The row carries no filter data: filters are
// pruned right after matching, only the chained header is kept.
func applyFilter(height int32, data []byte, block *wire.MsgBlock, prev chainhash.Hash) (storage.Filter, error) {
	var hdr, ok = chain.HeaderAt(height)
	if !ok {
		return storage.Filter{}, fmt.Errorf("no header at height %d", height)
	}
	var row = storage.Filter{Height: height, BlockHash: hdr.Hash, FilterHeader: spv.FilterHeader(spv.FilterHash(data), prev)}
	var watched = watchedScripts()
	var scriptBytes = make([][]byte, len(watched))
	for i, w := range watched {
		scriptBytes[i] = w.script
	}
	var hits, err = spv.MatchScripts(data, &hdr.Hash, scriptBytes)
	if err != nil {
		return row, fmt.Errorf("match filter at height %d: %w", height, err)
	}
	var matched []watchScript
	for i, hit := range hits {
		if hit { matched = append(matched, watched[i]) }
	}
	for _, w := range matched {
		log.Printf("match: filter of block %d (%s) matches address %s", height, hdr.Hash, w.address)
	}
	if len(matched) > 0 && block == nil {
		block, err = fetchBlock(height)
		if err != nil {
			return row, fmt.Errorf("download matched block %d: %w", height, err)
		}
	}
	if block != nil {
		var n, err = processBlock(height, block)
		if err != nil { return row, err }
		if len(matched) > 0 && n == 0 {
			log.Printf("match: block %d holds no wallet transaction (filter false positive)", height)
		}
	}
	for _, w := range matched {
		if err := store.SaveMatch(storage.Match{Height: height, BlockHash: hdr.Hash, Address: w.address, Script: w.script}); err != nil {
			return row, fmt.Errorf("store match at height %d: %w", height, err)
		}
	}
	return row, nil
}

func describeVariants(variants []*variant) string {
	var parts = make([]string, len(variants))
	for i, v := range variants {
		parts[i] = fmt.Sprintf("%s from %s", v.hash, peerList(v.peers))
	}
	return strings.Join(parts, ", ")
}

func peerList(peers []*conn) string {
	var addrs = make([]string, len(peers))
	for i, c := range peers {
		addrs[i] = c.addr
	}
	return strings.Join(addrs, " ")
}
