// Command hdwalk prints a step-by-step BIP39 → BIP32 → BIP84 key
// derivation for the official BIP84 test-vector mnemonic, showing every
// intermediate key and cross-checking the result against the values
// published in BIP84 (bip-0084.mediawiki).
//
// Run from the repository root:
//
//	go run ./cmd/hdwalk
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"fmt"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/tyler-smith/go-bip39"

	"spvbit/internal/wallet"
)

// The official BIP84 test vector mnemonic (mainnet).
const mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

// Expected values published in the BIP84 test vectors (mainnet).
const (
	specRootPub     = "zpub6jftahH18ngZxLmXaKw3GSZzZsszmt9WqedkyZdezFtWRFBZqsQH5hyUmb4pCEeZGmVfQuP5bedXTB8is6fTv19U1GQRyQUKQGUTzyHACMF"
	specAcctXPriv   = "zprvAdG4iTXWBoARxkkzNpNh8r6Qag3irQB8PzEMkAFeTRXxHpbF9z4QgEvBRmfvqWvGp42t42nvgGpNgYSJA9iefm1yYNZKEm7z6qUWCroSQnE"
	specAcctXPub    = "zpub6rFR7y4Q2AijBEqTUquhVz398htDFrtymD9xYYfG1m4wAcvPhXNfE3EfH1r1ADqtfSdVCToUG868RvUUkgDKf31mGDtKsAYz2oz2AGutZYs"
	specAddr0       = "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu"
	specPub0        = "0330d54fd0dd420a6e5f8d3624f5f3482cae350f79d5f0753bf5beef9c2d91af3c"
	specAddr1       = "bc1qnjg0jd8228aq7egyzacy8cys3knf9xvrerkf9g"
	specPub1        = "03e775fd51f0dfb8cd865d9ff1cca2a158cf651fe997fdc9fee9c1d3b5e995ea77"
	specChangeAddr  = "bc1q8c6fshw2dlwun7ekn9qwf37cu2rn755upcp6el"
	specSeed        = "5eb00bbddcf069084889a8ab9155568165f5c453ccb85e70811aaed6f6da5fc19a5ac40b389cd370d086206dec8aa6c43daea6690f20ad3d8d48b2d2ce9e38e4"
	specChecksumBit = "3" // SHA256(entropy)[0] >> 4 for this vector
)

func main() {
	net := &chaincfg.MainNetParams

	fmt.Println("== 0. BIP39: entropy -> mnemonic ==")
	entropy, err := bip39.EntropyFromMnemonic(mnemonic)
	must(err)
	h := sha256.Sum256(entropy)
	fmt.Printf("entropy (128 bits)          : %x\n", entropy)
	fmt.Printf("SHA256(entropy) first nibble: %x   -> appended as 4-bit checksum (spec: %s)\n", h[0]>>4, specChecksumBit)
	fmt.Printf("132 bits / 11 bits per word : 12 words\n")
	fmt.Printf("mnemonic                    : %s\n", mnemonic)

	fmt.Println("\n== 1. BIP39: seed = PBKDF2-HMAC-SHA512(mnemonic, salt=\"mnemonic\"+passphrase, 2048 iters) ==")
	seed := bip39.NewSeed(mnemonic, "")
	fmt.Printf("seed (64 bytes)             : %x\n", seed)
	fmt.Printf("matches BIP39 test vector   : %v\n", fmt.Sprintf("%x", seed) == specSeed)

	fmt.Println("\n== 2. BIP32: master key = HMAC-SHA512(key=\"Bitcoin seed\", data=seed) ==")
	mac := hmac.New(sha512.New, []byte("Bitcoin seed"))
	mac.Write(seed)
	I := mac.Sum(nil)
	IL, IR := I[:32], I[32:]
	fmt.Printf("IL = master private key (32B): %x\n", IL)
	fmt.Printf("IR = master chain code (32B) : %x\n", IR)
	master, err := hdkeychain.NewMaster(seed, net)
	must(err)
	fmt.Printf("master xprv (base58check)    : %s\n", master.String())
	fmt.Printf("master fingerprint           : %x  (= HASH160(master pubkey)[:4])\n", fingerprint(master))

	fmt.Println("\n== 3. BIP32 CKDpriv: walk m/84' -> m/84'/0' -> m/84'/0'/0' (all hardened) ==")
	fmt.Println("For hardened child i >= 2^31:  I = HMAC-SHA512(c_parent, 0x00 || k_parent || ser32(i));")
	fmt.Println("                               k_child = (IL + k_parent) mod n;   c_child = IR")
	key := master
	steps := []struct {
		name string
		idx  uint32
	}{
		{"purpose", 84}, {"coin_type", 0}, {"account", 0},
	}
	for n, s := range steps {
		child := hdkeychain.HardenedKeyStart + s.idx
		_, _, pChain, pPriv := decodeExt(key.String())
		key, err = key.Derive(child)
		must(err)
		depth, _, chain, priv := decodeExt(key.String())
		fmt.Printf("\n-- derive %s' (child %d = 2^31+%d) --\n", s.name, child, s.idx)
		if n == 0 { // show the actual HMAC-SHA512 for the first hardened step
			data := append([]byte{0x00}, pPriv...)
			data = append(data, ser32(child)...)
			hm := hmac.New(sha512.New, pChain)
			hm.Write(data)
			out := hm.Sum(nil)
			fmt.Printf("   HMAC key  (c_parent) : %x\n", pChain)
			fmt.Printf("   HMAC data (0x00||k||i): %x\n", data)
			fmt.Printf("   I = HMAC output      : %x\n", out)
			fmt.Printf("   IL (addend)          : %x\n", out[:32])
			fmt.Printf("   IR = new chain code  : %x\n", out[32:])
		}
		fmt.Printf("   depth=%d  private key: %x\n   chain code: %x\n", depth, priv, chain)
		// The spec's rootpub/rootpriv vector sits at the purpose level (m/84').
		if s.name == "purpose" {
			neutered, err := key.Neuter()
			must(err)
			z := toVersion(neutered.String(), [4]byte{0x04, 0xb2, 0x47, 0x46}) // zpub
			fmt.Printf("   m/84'  zpub: %s\n   matches BIP84 rootpub: %v\n", z, z == specRootPub)
		}
	}
	fmt.Printf("\naccount xprv (m/84'/0'/0')    : %s\n", key.String())
	zprv := toVersion(key.String(), [4]byte{0x04, 0xb2, 0x43, 0x0c}) // zprv
	fmt.Printf("account zprv (SLIP-132)       : %s\nmatches BIP84 xpriv vector: %v\n", zprv, zprv == specAcctXPriv)

	fmt.Println("\n== 4. Neuter: xprv -> zpub (drop private key, keep pubkey + chain code) ==")
	neutered, err := key.Neuter()
	must(err)
	fmt.Printf("account xpub (plain BIP32)    : %s\n", neutered.String())
	zpub := toVersion(neutered.String(), [4]byte{0x04, 0xb2, 0x47, 0x46})
	fmt.Printf("account zpub (SLIP-132)       : %s\nmatches BIP84 xpub vector: %v\n", zpub, zpub == specAcctXPub)

	fmt.Println("\n== 5. Watch-only: derive non-hardened children from the account xpub alone ==")
	watch, err := hdkeychain.NewKeyFromString(neutered.String())
	must(err)
	if _, err = watch.Derive(hdkeychain.HardenedKeyStart); err != nil {
		fmt.Printf("hardened child from xpub -> error: %v (by design)\n", err)
	}
	fmt.Println("Non-hardened: I = HMAC-SHA512(c_parent, serP(K_parent) || ser32(i));  K_child = point(IL) + K_parent")
	var watchPubs [2][]byte
	for i, idx := range []uint32{0, 1} {
		changeKey, err := watch.Derive(0) // 0 = external (receive) chain
		must(err)
		addrKey, err := changeKey.Derive(idx)
		must(err)
		pub, err := addrKey.ECPubKey()
		must(err)
		comp := pub.SerializeCompressed()
		watchPubs[i] = comp
		hash := btcutil.Hash160(comp)
		addr, err := btcutil.NewAddressWitnessPubKeyHash(hash, net)
		must(err)
		fmt.Printf("m/84'/0'/0'/0/%d: pubkey=%x  hash160=%x  address=%s\n", idx, comp, hash, addr)
	}
	changeKey, err := watch.Derive(1) // 1 = internal (change) chain
	must(err)
	addrKey, err := changeKey.Derive(0)
	must(err)
	pub, err := addrKey.ECPubKey()
	must(err)
	hash := btcutil.Hash160(pub.SerializeCompressed())
	changeAddr, err := btcutil.NewAddressWitnessPubKeyHash(hash, net)
	must(err)
	fmt.Printf("m/84'/0'/0'/1/0: (change) address=%s\n", changeAddr)

	fmt.Println("\n== 6. Cross-check against the BIP84 spec vectors ==")
	fmt.Printf("receive[0] pubkey == spec : %v\n", fmt.Sprintf("%x", watchPubs[0]) == specPub0)
	fmt.Printf("receive[1] pubkey == spec : %v\n", fmt.Sprintf("%x", watchPubs[1]) == specPub1)
	fmt.Printf("address[0] == %s : %v\n", specAddr0, specAddr0 == deriveAddr(net, watchPubs[0]))
	fmt.Printf("address[1] == %s : %v\n", specAddr1, specAddr1 == deriveAddr(net, watchPubs[1]))
	fmt.Printf("change[0] == %s  : %v\n", specChangeAddr, specChangeAddr == changeAddr.String())

	fmt.Println("\n== 7. Same results from this repo's wallet package (internal/wallet) ==")
	w, err := wallet.New(mnemonic, "", net)
	must(err)
	wzpub, err := w.AccountXPub()
	must(err)
	fmt.Printf("wallet.AccountXPub() == spec zpub: %v\n", wzpub == specAcctXPub)
	for _, idx := range []uint32{0, 1} {
		addr, path, pubkey, err := w.DeriveAddress(idx)
		must(err)
		fmt.Printf("wallet.DeriveAddress(%d): %s at %s (pubkey matches watch-only: %v)\n",
			idx, addr, path, fmt.Sprintf("%x", pubkey) == fmt.Sprintf("%x", watchPubs[idx]))
	}
}

// deriveAddr renders a compressed pubkey as a P2WPKH (BIP84) address.
func deriveAddr(net *chaincfg.Params, comp []byte) string {
	a, err := btcutil.NewAddressWitnessPubKeyHash(btcutil.Hash160(comp), net)
	must(err)
	return a.String()
}

// decodeExt unpacks a base58check-encoded extended key:
// 4B version | 1B depth | 4B parent fingerprint | 4B child number |
// 32B chain code | 33B key data (0x00 || 32B priv, or 33B pub) | 4B checksum.
func decodeExt(s string) (depth byte, child uint32, chainCode, keyData []byte) {
	raw := base58.Decode(s)
	if len(raw) != 82 {
		panic("unexpected extended key length")
	}
	return raw[4], binary.BigEndian.Uint32(raw[9:13]), raw[13:45], raw[46:78]
}

// fingerprint is the BIP32 master-key fingerprint: HASH160(master pubkey)[:4],
// as used in wallet descriptors like wpkh([d34db33f/84'/0'/0']zpub...).
func fingerprint(master *hdkeychain.ExtendedKey) []byte {
	pub, err := master.Neuter()
	must(err)
	k, err := pub.ECPubKey()
	must(err)
	return btcutil.Hash160(k.SerializeCompressed())[:4]
}

// toVersion re-encodes an extended key with different SLIP-132 version bytes
// (e.g. xpub 0x0488b21e -> zpub 0x04b24746), recomputing the base58check sum.
func toVersion(key string, newVersion [4]byte) string {
	raw := base58.Decode(key)
	if len(raw) != 82 {
		return key
	}
	payload := raw[4:78]
	out := make([]byte, 0, 82)
	out = append(out, newVersion[:]...)
	out = append(out, payload...)
	h1 := sha256.Sum256(out)
	h2 := sha256.Sum256(h1[:])
	out = append(out, h2[:4]...)
	return base58.Encode(out)
}

func ser32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
