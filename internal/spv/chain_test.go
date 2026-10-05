package spv

import "math/big"
import "testing"
import "time"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/btcsuite/btcd/chaincfg/chainhash"
import "github.com/btcsuite/btcd/wire"

// easyBits is a very low difficulty target so tests can mine headers fast.
const easyBits = 0x207fffff

// mineHeader solves the PoW of a header with a given parent, bits and time.
func mineHeader(prev chainhash.Hash, bits uint32, ts time.Time) wire.BlockHeader {
	var hdr = wire.BlockHeader{Version: 1, PrevBlock: prev, Bits: bits, Timestamp: ts}
	for {
		var target, err = compactToBig(bits)
		if err != nil { panic(err) }
		if hashToBig(hdr.BlockHash()).Cmp(target) <= 0 {
			return hdr
		}
		hdr.Nonce++
	}
}

// TestCompactRoundTrip checks the compact bits encoding against its inverse.
func TestCompactRoundTrip(t *testing.T) {
	for _, bits := range []uint32{0x1d00ffff, 0x1b0404cb, 0x207fffff, 0x04008000, 0x03000001} {
		var target, err = compactToBig(bits)
		if err != nil {
			t.Fatalf("compactToBig(%x): %v", bits, err)
		}
		if got := bigToCompact(target); got != bits {
			t.Errorf("bigToCompact(compactToBig(%x)) = %x", bits, got)
		}
	}
	var powLimit = chaincfg.MainNetParams.PowLimit
	var target = new(big.Int).Div(powLimit, big.NewInt(3))
	var got, err = compactToBig(bigToCompact(target))
	if err != nil {
		t.Fatalf("compactToBig: %v", err)
	}
	var eps = new(big.Int).Rsh(target, 23)
	if diff := new(big.Int).Sub(got, target); diff.Abs(diff).Cmp(eps) > 0 {
		t.Errorf("compact round trip of powLimit/3 = %x, want ~%x", got, target)
	}
}

// TestNextRetargetBits checks the difficulty adjustment formula and clamping
// against the canonical compact encoding of the expected targets.
func TestNextRetargetBits(t *testing.T) {
	var params = &chaincfg.MainNetParams
	var base = new(big.Int).Div(params.PowLimit, big.NewInt(4))
	var oldBits = bigToCompact(base)
	var first = time.Unix(1_700_000_000, 0)
	var last = first.Add(retargetTimespan / 2 * time.Second)
	var bits, err = nextRetargetBits(first, last, oldBits, params)
	if err != nil {
		t.Fatalf("nextRetargetBits: %v", err)
	}
	var wantBits = bigToCompact(new(big.Int).Div(base, big.NewInt(2)))
	if bits != wantBits {
		t.Errorf("bits after 2x-fast period = %x, want %x", bits, wantBits)
	}
	last = first.Add(8 * retargetTimespan * time.Second)
	bits, err = nextRetargetBits(first, last, oldBits, params)
	if err != nil {
		t.Fatalf("nextRetargetBits: %v", err)
	}
	wantBits = bigToCompact(new(big.Int).Mul(base, big.NewInt(4)))
	if bits != wantBits {
		t.Errorf("bits after clamped slow period = %x, want %x", bits, wantBits)
	}
}

// TestChainAdd builds a small valid chain on mainnet and checks the
// validation rejects bad PoW, bad linkage and bad timestamps.
func TestChainAdd(t *testing.T) {
	var params = &chaincfg.MainNetParams
	var c = NewChain(params)
	if err := c.Add(&params.GenesisBlock.Header); err != nil {
		t.Fatalf("add genesis: %v", err)
	}
	var ts = params.GenesisBlock.Header.Timestamp.Add(time.Minute)
	var prev = params.GenesisBlock.Header.BlockHash()
	for height := int32(1); height <= 3; height++ {
		var hdr = mineHeader(prev, easyBits, ts)
		if err := c.Add(&hdr); err != nil {
			t.Fatalf("add header %d: %v", height, err)
		}
		prev = hdr.BlockHash()
		ts = ts.Add(time.Minute)
	}
	if c.Height() != 3 {
		t.Fatalf("height = %d, want 3", c.Height())
	}
	var tip, ok = c.Tip()
	if !ok || tip.Hash != prev {
		t.Fatalf("tip = %v, %v; want hash %s", tip, ok, prev)
	}
	if c.HeightOf(prev) != 3 {
		t.Fatalf("HeightOf(tip) = %d, want 3", c.HeightOf(prev))
	}
	if len(c.Locator()) != 4 {
		t.Fatalf("locator has %d hashes, want 4", len(c.Locator()))
	}
	var bad = mineHeader(prev, easyBits, ts.Add(time.Minute))
	bad.Bits = 0x1d00ffff
	if err := c.Add(&bad); err == nil {
		t.Fatal("header with invalid PoW accepted")
	}
	bad = mineHeader(prev, easyBits, ts.Add(time.Minute))
	bad.PrevBlock = chainhash.Hash{}
	if err := c.Add(&bad); err == nil {
		t.Fatal("header with broken linkage accepted")
	}
	bad = mineHeader(prev, easyBits, ts.Add(-time.Hour))
	if err := c.Add(&bad); err == nil {
		t.Fatal("header with stale timestamp accepted")
	}
	bad = mineHeader(prev, easyBits, time.Now().Add(3*time.Hour))
	if err := c.Add(&bad); err == nil {
		t.Fatal("header with future timestamp accepted")
	}
}

// TestTestnetMinDifficulty checks the 20-minute min-difficulty consensus
// rule of testnet3 and testnet4.
func TestTestnetMinDifficulty(t *testing.T) {
	for _, base := range []chaincfg.Params{chaincfg.TestNet3Params, chaincfg.TestNet4Params} {
		var params = base
		params.PowLimitBits = easyBits
		var c = NewChain(&params)
		if err := c.Add(&params.GenesisBlock.Header); err != nil {
			t.Fatalf("%s: add genesis: %v", params.Name, err)
		}
		var prev = params.GenesisBlock.Header.BlockHash()
		var late = params.GenesisBlock.Header.Timestamp.Add(21 * time.Minute)
		var hdr = mineHeader(prev, params.PowLimitBits, late)
		if err := c.Add(&hdr); err != nil {
			t.Fatalf("%s: min-difficulty header after 21 minutes rejected: %v", params.Name, err)
		}
		var early = late.Add(time.Second)
		hdr = mineHeader(hdr.BlockHash(), params.PowLimitBits, early)
		if err := c.Add(&hdr); err == nil {
			t.Fatalf("%s: min-difficulty header without the required gap accepted", params.Name)
		}
	}
}

// trustedChain builds a chain of the network with n headers after genesis,
// ten minutes apart, without mining: bitsAt gives the bits at each height.
func trustedChain(t *testing.T, params *chaincfg.Params, n int, bitsAt func(height int) uint32) *Chain {
	var c = NewChain(params)
	if err := c.AppendTrusted(&params.GenesisBlock.Header); err != nil {
		t.Fatalf("append genesis: %v", err)
	}
	var genesis = params.GenesisBlock.Header.Timestamp
	var prev = params.GenesisBlock.Header.BlockHash()
	for height := 1; height <= n; height++ {
		var bits = bitsAt(height)
		var hdr = wire.BlockHeader{Version: 4, PrevBlock: prev, Bits: bits, Timestamp: genesis.Add(time.Duration(height) * 10 * time.Minute)}
		if err := c.AppendTrusted(&hdr); err != nil {
			t.Fatalf("append header %d: %v", height, err)
		}
		prev = hdr.BlockHash()
	}
	return c
}

// TestTestnetDifficultyAtLimit checks that testnet3 and testnet4 accept
// blocks less than 20 minutes apart at the minimum difficulty when it is the
// difficulty of the period, as at the start of testnet4.
func TestTestnetDifficultyAtLimit(t *testing.T) {
	for _, base := range []chaincfg.Params{chaincfg.TestNet3Params, chaincfg.TestNet4Params} {
		var params = base
		params.PowLimitBits = easyBits
		var c = trustedChain(t, &params, retargetInterval+1, func(int) uint32 { return easyBits })
		var parent, _ = c.Tip()
		var hdr = mineHeader(parent.Hash, easyBits, parent.Timestamp.Add(time.Minute))
		if err := c.Add(&hdr); err != nil {
			t.Fatalf("%s: header at the period's minimum difficulty rejected: %v", params.Name, err)
		}
	}
}

// TestRetargetBIP94 checks the testnet4 difficulty retarget: it scales the
// bits of the period's first block even when its last block has the minimum
// difficulty, bans the minimum difficulty at the retarget and rejects a
// timestamp more than 10 minutes before the parent's.
func TestRetargetBIP94(t *testing.T) {
	const periodBits = 0x2000ffff
	var params = chaincfg.TestNet4Params
	params.PowLimitBits = easyBits
	params.PowLimit, _ = compactToBig(easyBits)
	var last = 2*retargetInterval - 1
	var c = trustedChain(t, &params, last, func(height int) uint32 {
		if height == last { return easyBits }
		return periodBits
	})
	var first, _ = c.HeaderAt(retargetInterval)
	var parent, _ = c.Tip()
	var want, err = nextRetargetBits(first.Timestamp, parent.Timestamp, periodBits, &params)
	if err != nil { t.Fatal(err) }
	var testnet3, _ = nextRetargetBits(first.Timestamp, parent.Timestamp, easyBits, &params)
	if want == testnet3 {
		t.Fatalf("test chain does not tell BIP94 from testnet3: both retarget to %x", want)
	}
	var late = parent.Timestamp.Add(21 * time.Minute)
	var hdr = mineHeader(parent.Hash, easyBits, late)
	if err := c.Add(&hdr); err == nil {
		t.Fatal("min-difficulty header at the retarget accepted")
	}
	hdr = mineHeader(parent.Hash, testnet3, late)
	if err := c.Add(&hdr); err == nil {
		t.Fatal("retarget from the last block's bits accepted")
	}
	hdr = mineHeader(parent.Hash, want, parent.Timestamp.Add(-11*time.Minute))
	if err := c.Add(&hdr); err == nil {
		t.Fatal("time-warped retarget header accepted")
	}
	hdr = mineHeader(parent.Hash, want, parent.Timestamp.Add(-9*time.Minute))
	if err := c.Add(&hdr); err != nil {
		t.Fatalf("retarget header rejected: %v", err)
	}
}
