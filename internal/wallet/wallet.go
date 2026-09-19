// Package wallet implements a BIP39/BIP84 hierarchical-deterministic
// Bitcoin wallet that produces native SegWit (P2WPKH, bech32) addresses.
package wallet

import "bytes"
import "crypto/sha256"
import "fmt"
import "strings"
import "github.com/btcsuite/btcd/btcutil"
import "github.com/btcsuite/btcd/btcutil/base58"
import "github.com/btcsuite/btcd/btcutil/hdkeychain"
import "github.com/btcsuite/btcd/chaincfg"
import "github.com/tyler-smith/go-bip39"

// Purpose is BIP84: native SegWit, derivation m/84'/coin'/account'/change/index.
const Purpose = 84

// Wallet is a BIP84 HD wallet for account 0, external (receive) chain.
type Wallet struct {
	net      *chaincfg.Params
	coin     uint32
	mnemonic string
	master   *hdkeychain.ExtendedKey
}

// NewMnemonic generates a fresh BIP39 mnemonic with the given entropy size
// (128, 160, 192, 224 or 256 bits). 128 bits (12 words) is the default.
func NewMnemonic(bits int) (string, error) {
	var entropy, err = bip39.NewEntropy(bits)
	if err != nil {
		return "", fmt.Errorf("generate entropy: %w", err)
	}
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return "", fmt.Errorf("encode mnemonic: %w", err)
	}
	return mnemonic, nil
}

// New restores (or, for a freshly generated mnemonic, creates) a wallet for
// the given network. The passphrase is the optional BIP39 seed passphrase
// (empty string means none).
func New(mnemonic, passphrase string, net *chaincfg.Params) (*Wallet, error) {
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, fmt.Errorf("invalid BIP39 mnemonic")
	}
	var seed = bip39.NewSeed(mnemonic, passphrase)
	var master, err = hdkeychain.NewMaster(seed, net)
	if err != nil {
		return nil, fmt.Errorf("derive master key: %w", err)
	}
	return &Wallet{
		net:      net,
		coin:     coinType(net),
		mnemonic: mnemonic,
		master:   master,
	}, nil
}

// Mnemonic returns the wallet seed phrase. Callers must treat it as secret
// material and only persist it inside the encrypted database.
func (w *Wallet) Mnemonic() string { return w.mnemonic }

// Net returns the chain parameters of the wallet.
func (w *Wallet) Net() *chaincfg.Params { return w.net }

// AccountXPub returns the neutered account-level extended public key
// (m/84'/coin'/0'), which is safe to store and use for watch-only purposes.
// It is encoded with the SLIP-132 zpub/vpub version bytes for BIP84.
func (w *Wallet) AccountXPub() (string, error) {
	var key, err = w.derivePath([]uint32{harden(Purpose), harden(w.coin), harden(0)})
	if err != nil { return "", err }
	neutered, err := key.Neuter()
	if err != nil { return "", fmt.Errorf("neuter account key: %w", err) }
	xpub, err := toSLIP132(neutered.String())
	if err != nil { return "", err }
	return xpub, nil
}

// DeriveAddress derives the P2WPKH address at index on the external chain
// and returns the bech32 address, its derivation path and the compressed
// public key.
func (w *Wallet) DeriveAddress(index uint32) (address, path string, pubkey []byte, err error) {
	path = fmt.Sprintf("m/%d'/%d'/%d'/%d/%d", Purpose, w.coin, 0, 0, index)
	key, err := w.derivePath([]uint32{harden(Purpose), harden(w.coin), harden(0), 0, index})
	if err != nil { return "", "", nil, err }
	pub, err := key.ECPubKey()
	if err != nil {
		return "", "", nil, fmt.Errorf("public key: %w", err)
	}
	pubkey = pub.SerializeCompressed()
	var hash = btcutil.Hash160(pubkey)
	addr, err := btcutil.NewAddressWitnessPubKeyHash(hash, w.net)
	if err != nil {
		return "", "", nil, fmt.Errorf("encode address: %w", err)
	}
	return addr.String(), path, pubkey, nil
}

func (w *Wallet) derivePath(steps []uint32) (*hdkeychain.ExtendedKey, error) {
	var key = w.master
	for _, step := range steps {
		var err error
		key, err = key.Derive(step)
		if err != nil {
			return nil, fmt.Errorf("derive step %d: %w", step, err)
		}
	}
	return key, nil
}

func harden(v uint32) uint32 { return v + hdkeychain.HardenedKeyStart }

// SLIP-132 extended public key version bytes.
var (
	xpubVersion = [4]byte{0x04, 0x88, 0xb2, 0x1e} // xpub (mainnet)
	zpubVersion = [4]byte{0x04, 0xb2, 0x47, 0x46} // zpub (mainnet, BIP84)
	tpubVersion = [4]byte{0x04, 0x35, 0x87, 0xcf} // tpub (testnet)
	vpubVersion = [4]byte{0x04, 0x5f, 0x1c, 0xf6} // vpub (testnet, BIP84)
)

// toSLIP132 re-encodes a BIP32 extended public key with the SLIP-132 version
// bytes appropriate for BIP84. btcd's hdkeychain always uses the plain
// xpub/tpub versions; the 78-byte payload is unchanged and the base58check
// checksum is recomputed for the new version, producing the zpub/vpub strings
// modern wallets expect for BIP84 accounts. Keys with unknown version bytes
// are returned unchanged.
func toSLIP132(xpub string) (string, error) {
	var serialized = base58.Decode(xpub)
	if len(serialized) != 82 {
		return xpub, nil
	}
	var version, payload = serialized[:4], serialized[4 : len(serialized)-4]
	var newVersion []byte
	switch {
	case bytes.Equal(version, xpubVersion[:]):
		newVersion = zpubVersion[:]
	case bytes.Equal(version, tpubVersion[:]):
		newVersion = vpubVersion[:]
	default:
		return xpub, nil
	}
	var sum = checksum(newVersion, payload)
	var out = make([]byte, 0, 82)
	out = append(out, newVersion...)
	out = append(out, payload...)
	out = append(out, sum...)
	return base58.Encode(out), nil
}

// checksum is the 4-byte double-SHA256 base58check checksum.
func checksum(parts ...[]byte) []byte {
	var h = sha256.New()
	for _, p := range parts { h.Write(p) }
	var first = h.Sum(nil)
	var h2 = sha256.Sum256(first)
	return h2[:4]
}

// coinType returns the BIP44/BIP84 coin type for a network: 0' for mainnet,
// 1' for the test networks.
func coinType(net *chaincfg.Params) uint32 {
	if net.Net == chaincfg.MainNetParams.Net { return 0 }
	return 1
}

// ParamsForNetwork maps a user-facing network name to chain parameters.
func ParamsForNetwork(network string) (*chaincfg.Params, error) {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "mainnet", "bitcoin":  return &chaincfg.MainNetParams, nil
	case "testnet", "testnet3": return &chaincfg.TestNet3Params, nil
	case "regtest":             return &chaincfg.RegressionNetParams, nil
	case "simnet":              return &chaincfg.SimNetParams, nil
	default:               		return nil, fmt.Errorf("unknown network %q (want mainnet, testnet, regtest or simnet)", network)
	}
}
