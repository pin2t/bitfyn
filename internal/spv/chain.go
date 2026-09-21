// Package spv implements the SPV sync engine: a validated block header
// chain, BIP158 basic filter download and wallet script matching.
package spv

import "fmt"
import "math/big"
import "slices"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"

// maxFutureDrift bounds how far ahead of local time block timestamps may be.
const maxFutureDrift = 2 * time.Hour

// medianWindow is the number of past blocks used for the timestamp median.
const medianWindow = 11

// retargetInterval is the block count between difficulty adjustments.
const retargetInterval = 2016

// retargetTimespan is the expected time for a full difficulty period.
const retargetTimespan = retargetInterval * 10 * 60

// testnetMinGap is the delay after which testnet allows a min-difficulty block.
const testnetMinGap = 20 * 60

// Header is a validated block header at a known height.
type Header struct {
	Hash       chainhash.Hash
	Height     int32
	Version    int32
	PrevBlock  chainhash.Hash
	MerkleRoot chainhash.Hash
	Timestamp  time.Time
	Bits       uint32
	Nonce      uint32
}

// Chain is an ordered, validated block header chain.
type Chain struct {
	params  *chaincfg.Params
	headers []Header
	byHash  map[chainhash.Hash]int32
}

// NewChain creates an empty header chain for the network.
func NewChain(params *chaincfg.Params) *Chain {
	return &Chain{params: params, byHash: make(map[chainhash.Hash]int32)}
}

// Height returns the tip height, or -1 for an empty chain.
func (c *Chain) Height() int32 { return int32(len(c.headers)) - 1 }

// HeightOf returns the height of the given header hash, or -1 if unknown.
func (c *Chain) HeightOf(hash chainhash.Hash) int32 {
	if height, ok := c.byHash[hash]; ok { return height }
	return -1
}

// Tip returns the tip header.
func (c *Chain) Tip() (Header, bool) {
	if len(c.headers) == 0 { return Header{}, false }
	return c.headers[len(c.headers)-1], true
}

// HeaderAt returns the header at the given height.
func (c *Chain) HeaderAt(height int32) (Header, bool) {
	if height < 0 || int(height) >= len(c.headers) { return Header{}, false }
	return c.headers[height], true
}

// Locator builds a block locator from the tip back to genesis with
// exponential backoff, for getheaders requests.
func (c *Chain) Locator() []*chainhash.Hash {
	if len(c.headers) == 0 {
		var zero = chainhash.Hash{}
		return []*chainhash.Hash{&zero}
	}
	var locator = make([]*chainhash.Hash, 0, 32)
	var step = int32(1)
	for height := c.Height(); height >= 0; height -= step {
		var hash = c.headers[height].Hash
		locator = append(locator, &hash)
		if len(locator) > 10 { step *= 2 }
	}
	return locator
}

// Add validates the header as the direct successor of the tip and appends it.
func (c *Chain) Add(hdr *wire.BlockHeader) error {
	var height = int32(len(c.headers))
	var hash = hdr.BlockHash()
	if err := checkPoW(hdr, hash); err != nil {
		return fmt.Errorf("header %s at height %d: %w", hash, height, err)
	}
	if height == 0 {
		var genesis = c.params.GenesisBlock.Header.BlockHash()
		if hash != genesis {
			return fmt.Errorf("first header %s, want genesis %s", hash, genesis)
		}
	} else {
		var tip = c.headers[height-1]
		if hdr.PrevBlock != tip.Hash {
			return fmt.Errorf("header %s at height %d: prev block %s, want %s", hash, height, hdr.PrevBlock, tip.Hash)
		}
		var median, err = c.medianTime()
		if err != nil { return err }
		if !hdr.Timestamp.After(median) {
			return fmt.Errorf("header %s at height %d: timestamp %s not after median %s", hash, height, hdr.Timestamp, median)
		}
		if hdr.Timestamp.After(time.Now().Add(maxFutureDrift)) {
			return fmt.Errorf("header %s at height %d: timestamp %s too far in the future", hash, height, hdr.Timestamp)
		}
		if err := c.checkDifficulty(hdr, height); err != nil {
			return fmt.Errorf("header %s at height %d: %w", hash, height, err)
		}
	}
	c.headers = append(c.headers, Header{
		Hash: hash, Height: height, Version: hdr.Version, PrevBlock: hdr.PrevBlock,
		MerkleRoot: hdr.MerkleRoot, Timestamp: hdr.Timestamp, Bits: hdr.Bits, Nonce: hdr.Nonce,
	})
	c.byHash[hash] = height
	return nil
}

// appendTrusted appends a header that was validated before it was persisted.
// The database is the wallet's own, so the full checks are skipped for speed
// when reloading; only the prev-block linkage is verified.
func (c *Chain) appendTrusted(hdr *wire.BlockHeader) error {
	var height = int32(len(c.headers))
	var hash = hdr.BlockHash()
	if height == 0 {
		var genesis = c.params.GenesisBlock.Header.BlockHash()
		if hash != genesis {
			return fmt.Errorf("first header %s, want genesis %s", hash, genesis)
		}
	} else if hdr.PrevBlock != c.headers[height-1].Hash {
		return fmt.Errorf("stored header %s at height %d: prev block %s, want %s", hash, height, hdr.PrevBlock, c.headers[height-1].Hash)
	}
	c.headers = append(c.headers, Header{
		Hash: hash, Height: height, Version: hdr.Version, PrevBlock: hdr.PrevBlock,
		MerkleRoot: hdr.MerkleRoot, Timestamp: hdr.Timestamp, Bits: hdr.Bits, Nonce: hdr.Nonce,
	})
	c.byHash[hash] = height
	return nil
}

// Truncate removes every header above the given height.
func (c *Chain) Truncate(height int32) {
	for i := height + 1; i < int32(len(c.headers)); i++ {
		delete(c.byHash, c.headers[i].Hash)
	}
	c.headers = c.headers[:height+1]
}

func (c *Chain) medianTime() (time.Time, error) {
	var n = len(c.headers)
	if n == 0 {
		return time.Time{}, fmt.Errorf("median time of empty chain")
	}
	var times = make([]int64, 0, medianWindow)
	for i := n - 1; i >= 0 && len(times) < medianWindow; i-- {
		times = append(times, c.headers[i].Timestamp.Unix())
	}
	slices.Sort(times)
	return time.Unix(times[len(times)/2], 0), nil
}

func (c *Chain) checkDifficulty(hdr *wire.BlockHeader, height int32) error {
	switch c.params.Net {
	case chaincfg.RegressionNetParams.Net, chaincfg.SimNetParams.Net:
		return nil
	case chaincfg.TestNet3Params.Net:
		if hdr.Bits == c.params.PowLimitBits {
			var prev = c.headers[height-1]
			if hdr.Timestamp.Unix()-prev.Timestamp.Unix() > testnetMinGap {
				return nil
			}
			return fmt.Errorf("min-difficulty bits %x without the required %d-second gap", hdr.Bits, testnetMinGap)
		}
	}
	if height%retargetInterval == 0 {
		var first = c.headers[height-retargetInterval].Timestamp
		var parent = c.headers[height-1]
		var want, err = nextRetargetBits(first, parent.Timestamp, parent.Bits, c.params)
		if err != nil { return err }
		if hdr.Bits != want {
			return fmt.Errorf("bits %x at difficulty retarget, want %x", hdr.Bits, want)
		}
	}
	return nil
}

// checkPoW verifies the header hash meets the difficulty target encoded in
// its bits field.
func checkPoW(hdr *wire.BlockHeader, hash chainhash.Hash) error {
	if hdr.Bits == 0 {
		return fmt.Errorf("zero difficulty bits")
	}
	var target, err = compactToBig(hdr.Bits)
	if err != nil { return err }
	if target.Sign() <= 0 {
		return fmt.Errorf("non-positive target %s", target)
	}
	var num = hashToBig(hash)
	if num.Cmp(target) > 0 {
		return fmt.Errorf("hash %064x exceeds target %064x", num, target)
	}
	return nil
}

// hashToBig converts a chain hash into the numeric form used for PoW
// comparisons: the stored bytes are little-endian, so they are reversed.
func hashToBig(hash chainhash.Hash) *big.Int {
	var bytes = hash
	for i, j := 0, len(bytes)-1; i < j; i, j = i+1, j-1 {
		bytes[i], bytes[j] = bytes[j], bytes[i]
	}
	return new(big.Int).SetBytes(bytes[:])
}

// nextRetargetBits computes the bits field of the first block after a
// difficulty period: the previous target scaled by the actual time the
// period took, clamped to a factor of four in either direction.
func nextRetargetBits(first, last time.Time, oldBits uint32, params *chaincfg.Params) (uint32, error) {
	var actual = last.Unix() - first.Unix()
	if actual < retargetTimespan/4 { actual = retargetTimespan / 4 }
	if actual > retargetTimespan*4 { actual = retargetTimespan * 4 }
	var target, err = compactToBig(oldBits)
	if err != nil { return 0, err }
	target.Mul(target, big.NewInt(actual))
	target.Div(target, big.NewInt(retargetTimespan))
	if target.Cmp(params.PowLimit) > 0 { target.Set(params.PowLimit) }
	return bigToCompact(target), nil
}

// compactToBig decodes the compact bits format into a target integer.
func compactToBig(compact uint32) (*big.Int, error) {
	var exponent = uint(compact >> 24)
	var mantissa = big.NewInt(int64(compact & 0x007fffff))
	var negative = compact&0x00800000 != 0
	if exponent <= 3 {
		mantissa.Rsh(mantissa, 8*(3-exponent))
	} else {
		mantissa.Lsh(mantissa, 8*(exponent-3))
	}
	if negative { mantissa.Neg(mantissa) }
	if mantissa.Sign() < 0 {
		return nil, fmt.Errorf("negative target from bits %x", compact)
	}
	if compact&0x007fffff != 0 && exponent == 0 {
		return nil, fmt.Errorf("invalid bits %x: zero exponent with non-zero mantissa", compact)
	}
	return mantissa, nil
}

// bigToCompact encodes a target integer in the compact bits format.
func bigToCompact(target *big.Int) uint32 {
	if target.Sign() <= 0 { return 0 }
	var exponent = uint32((target.BitLen() + 7) / 8)
	var mantissa = new(big.Int).Set(target)
	if exponent > 3 {
		mantissa.Rsh(mantissa, uint(8*(exponent-3)))
	} else {
		exponent = 3
	}
	if mantissa.BitLen() > 23 {
		mantissa.Rsh(mantissa, 8)
		exponent++
	}
	return exponent<<24 | uint32(mantissa.Int64()&0x007fffff)
}
