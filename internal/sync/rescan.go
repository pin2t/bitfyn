package sync

import "fmt"
import "log"
import "sort"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"
import "bitfyn/internal/spv"
import "bitfyn/internal/storage"

// rescanTarget is one queued address with the height its rescan starts at.
type rescanTarget struct {
	job    storage.Rescan
	from   int32
	script watchScript
}

// matchKey identifies a recorded filter match of an address in a block.
type matchKey struct {
	height  int32
	address string
}

// rescan matches the synced blocks again for the queued addresses: those
// watched after blocks paying them were scanned, as a change address another
// wallet on the same seed used first. Their coins were found late, from the
// stored transactions, so spends of them in the blocks scanned before were
// missed. Each address is rescanned from the first stored block paying it up
// to the last synced filter: the pruned filters are downloaded again and
// checked against the stored filter header chain, and every block matching
// an address it was not matched for before is processed. A queued address
// never paid in a stored block has nothing to rescan.
func rescan() error {
	var jobs, err = store.Rescans()
	if err != nil {
		return fmt.Errorf("load rescans: %w", err)
	}
	if len(jobs) == 0 {
		return nil
	}
	resume, err := store.FilterResumeHeight(filterStart)
	if err != nil {
		return fmt.Errorf("filter resume height: %w", err)
	}
	var end = resume - 1
	var targets, idle = rescanTargets(jobs, end)
	for _, j := range idle {
		if err := store.DeleteRescan(j); err != nil {
			return fmt.Errorf("drop rescan of %s: %w", j.Address, err)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	matched, err := matchedSet()
	if err != nil {
		return err
	}
	var start = targets[0].from
	log.Printf("sync: rescanning blocks %d-%d for %d addresses watched after they were paid", start, end, len(targets))
	for fStart := start; fStart <= end; fStart += filterBatch {
		var fEnd = min(fStart+filterBatch-1, end)
		report(StateFilters, fStart)
		var filters, err = refetchFilters(fStart, fEnd)
		if err != nil {
			return fmt.Errorf("filters %d-%d: %w", fStart, fEnd, err)
		}
		for i, data := range filters {
			if err := rescanBlock(fStart+int32(i), data, targets, matched, fetchBlock); err != nil {
				return err
			}
		}
	}
	for _, t := range targets {
		if err := store.DeleteRescan(t.job); err != nil {
			return fmt.Errorf("finish rescan of %s: %w", t.job.Address, err)
		}
	}
	log.Printf("sync: rescan of blocks %d-%d done", start, end)
	return nil
}

// rescanTargets splits the queued rescans into the targets to scan up to the
// end height, the lowest start first, and the rescans with nothing to scan.
// A target starts at the first stored block paying its address, and not
// below its queued height or the filter start.
func rescanTargets(jobs []storage.Rescan, end int32) ([]rescanTarget, []storage.Rescan) {
	walletMu.Lock()
	defer walletMu.Unlock()
	var byAddress = make(map[string]watchScript, len(scripts))
	for _, w := range scripts {
		byAddress[w.address] = w
	}
	var targets []rescanTarget
	var idle []storage.Rescan
	for _, j := range jobs {
		var w, watched = byAddress[j.Address]
		var first, paid = firstPaid[j.Address]
		var from = max(j.From, first, filterStart)
		if !watched || !paid || from > end {
			idle = append(idle, j)
			continue
		}
		targets = append(targets, rescanTarget{job: j, from: from, script: w})
	}
	sort.SliceStable(targets, func(i, k int) bool { return targets[i].from < targets[k].from })
	return targets, idle
}

// matchedSet returns the recorded filter matches.
func matchedSet() (map[matchKey]bool, error) {
	var list, err = store.Matches()
	if err != nil {
		return nil, fmt.Errorf("load matches: %w", err)
	}
	var set = make(map[matchKey]bool, len(list))
	for _, m := range list {
		set[matchKey{m.Height, m.Address}] = true
	}
	return set, nil
}

// rescanBlock matches the filter of the block at the height against the
// targets started at or below it. A block matched for an address before was
// processed while the address was watched and is skipped for it; when any
// other target matches, the block is downloaded with getBlock and its wallet
// transactions are stored.
func rescanBlock(height int32, data []byte, targets []rescanTarget, matched map[matchKey]bool, getBlock func(int32) (*wire.MsgBlock, error)) error {
	var hdr, ok = chain.HeaderAt(height)
	if !ok {
		return fmt.Errorf("no header at height %d", height)
	}
	var active []watchScript
	var scriptBytes [][]byte
	for _, t := range targets {
		if t.from > height { continue }
		active = append(active, t.script)
		scriptBytes = append(scriptBytes, t.script.script)
	}
	if len(active) == 0 {
		return nil
	}
	var hits, err = spv.MatchScripts(data, &hdr.Hash, scriptBytes)
	if err != nil {
		return fmt.Errorf("match filter at height %d: %w", height, err)
	}
	var fresh []watchScript
	for i, hit := range hits {
		if hit && !matched[matchKey{height, active[i].address}] {
			fresh = append(fresh, active[i])
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	for _, w := range fresh {
		log.Printf("match: rescanned filter of block %d (%s) matches address %s", height, hdr.Hash, w.address)
	}
	block, err := getBlock(height)
	if err != nil {
		return fmt.Errorf("download matched block %d: %w", height, err)
	}
	n, err := processBlock(height, block)
	if err != nil {
		return err
	}
	if n == 0 {
		log.Printf("match: block %d holds no wallet transaction (filter false positive)", height)
	}
	for _, w := range fresh {
		if err := store.SaveMatch(storage.Match{Height: height, BlockHash: hdr.Hash, Address: w.address, Script: w.script}); err != nil {
			return fmt.Errorf("store match at height %d: %w", height, err)
		}
		matched[matchKey{height, w.address}] = true
	}
	return nil
}

// refetchFilters downloads the filters of the synced blocks start..end again
// from the connected peers in turn, the primary first, and returns the first
// set whose chained headers match the stored filter header chain. Below the
// first stored header the peer's cfheaders supply the previous header; a
// wrong one cannot link to the stored chain either. A peer serving filters
// that do not link is disconnected.
func refetchFilters(start, end int32) ([][]byte, error) {
	var stopHdr, ok = chain.HeaderAt(end)
	if !ok {
		return nil, fmt.Errorf("no header at height %d", end)
	}
	var prev = chainhash.Hash{}
	var known = start == 0
	if !known {
		var stored, found, err = store.FilterHeaderAt(start - 1)
		if err != nil { return nil, err }
		prev, known = stored, found
	}
	var peers = orderedPeers()
	if len(peers) == 0 {
		return nil, fmt.Errorf("no connected peers")
	}
	var lastErr error
	for _, c := range peers {
		var from = prev
		if !known {
			var resp, err = c.getCFHeaders(start, stopHdr.Hash, requestTimeout)
			if err != nil {
				lastErr = err
				continue
			}
			from = resp.PrevFilterHeader
		}
		var filters, err = c.getCFilters(start, stopHdr.Hash)
		if err != nil {
			lastErr = err
			continue
		}
		if err := checkStoredChain(start, from, filters); err != nil {
			c.drop("rescan filters", err)
			lastErr = err
			continue
		}
		return filters, nil
	}
	return nil, fmt.Errorf("no peer served filters linking to the stored headers: %w", lastErr)
}

// checkStoredChain verifies that the filters from the height on, chained
// from prev, reproduce the stored filter headers.
func checkStoredChain(start int32, prev chainhash.Hash, filters [][]byte) error {
	for i, data := range filters {
		var height = start + int32(i)
		var stored, ok, err = store.FilterHeaderAt(height)
		if err != nil { return err }
		if !ok {
			return fmt.Errorf("filter header at height %d not stored", height)
		}
		prev = spv.FilterHeader(spv.FilterHash(data), prev)
		if prev != stored {
			return fmt.Errorf("filter at height %d chains to header %s, stored %s", height, prev, stored)
		}
	}
	return nil
}
