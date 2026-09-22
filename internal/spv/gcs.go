package spv

import "crypto/sha256"
import "fmt"
import "github.com/btcsuite/btcd/btcutil/gcs"
import "github.com/btcsuite/btcd/chaincfg/chainhash"

// BIP158 basic filter parameters: Golomb-Rice P and the false positive rate
// target M, matching the basic filter type deployed on the network.
const (
	gcsP = 19
	gcsM = 784931
)

// filterKey is the 16-byte siphash key of a block's basic filter: the first
// half of the block hash, per BIP158.
func filterKey(blockHash *chainhash.Hash) [16]byte {
	var key [16]byte
	copy(key[:], blockHash[:16])
	return key
}

// filterHash is the double SHA256 of a serialized filter, the value peers
// send in the cfheaders filter_hashes list and use to commit to the filter
// data.
func filterHash(data []byte) chainhash.Hash {
	var first = sha256.Sum256(data)
	return chainhash.Hash(sha256.Sum256(first[:]))
}

// filterHeader chains a raw filter hash to the previous filter header:
// SHA256d(raw || prev), per BIP157. The genesis filter chains to the null
// hash. This is the value peers send as cfheaders.prev_filter_header.
func filterHeader(raw, prev chainhash.Hash) chainhash.Hash {
	var buf [64]byte
	copy(buf[:32], raw[:])
	copy(buf[32:], prev[:])
	var first = sha256.Sum256(buf[:])
	return chainhash.Hash(sha256.Sum256(first[:]))
}

// matchScripts decodes a serialized BIP158 basic filter and reports which of
// the scripts are present in it.
func matchScripts(data []byte, blockHash *chainhash.Hash, scripts [][]byte) ([]bool, error) {
	var filter, err = gcs.FromNBytes(gcsP, gcsM, data)
	if err != nil {
		return nil, fmt.Errorf("decode filter: %w", err)
	}
	var key = filterKey(blockHash)
	var hits = make([]bool, len(scripts))
	for i, script := range scripts {
		var hit, err = filter.Match(key, script)
		if err != nil {
			return nil, fmt.Errorf("match script %d: %w", i, err)
		}
		hits[i] = hit
	}
	return hits, nil
}
