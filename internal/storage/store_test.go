package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPlainLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")

	s, err := Open(path, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Meta(); !errors.Is(err, ErrNoWallet) {
		t.Fatalf("Meta on fresh store = %v, want ErrNoWallet", err)
	}

	if err := s.SaveMeta("abandon " /* mnemonic */, "xpub-test", "testnet", 1234); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}
	meta, err := s.Meta()
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if meta.Network != "testnet" || meta.XPub != "xpub-test" || meta.CreatedAt != 1234 || meta.NextIndex != 0 {
		t.Errorf("unexpected meta: %+v", meta)
	}

	if err := s.AddAddress(0, "m/84'/1'/0'/0/0", "tb1qtest", []byte{1, 2, 3}); err != nil {
		t.Fatalf("AddAddress: %v", err)
	}
	n, err := s.CountAddresses()
	if err != nil || n != 1 {
		t.Fatalf("CountAddresses = %d, %v; want 1", n, err)
	}
	// Idempotent per index.
	if err := s.AddAddress(0, "m/84'/1'/0'/0/0", "tb1qtest", []byte{1, 2, 3}); err != nil {
		t.Fatalf("AddAddress again: %v", err)
	}
	if err := s.UpdateNextIndex(1); err != nil {
		t.Fatalf("UpdateNextIndex: %v", err)
	}
	s.Close()

	// Reopen: data persists.
	s2, err := Open(path, "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	meta2, err := s2.Meta()
	if err != nil {
		t.Fatalf("Meta after reopen: %v", err)
	}
	if meta2.NextIndex != 1 || meta2.Mnemonic != "abandon " {
		t.Errorf("unexpected meta after reopen: %+v", meta2)
	}
}

func TestEncryptedLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.db")
	const pass = "s3cret-passphrase"

	s, err := Open(path, pass)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.SaveMeta("secret words", "xpub-secret", "mainnet", 99); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}
	s.Close()

	// The on-disk file must not expose the plaintext SQLite header.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(raw) >= 16 && string(raw[:16]) == "SQLite format 3\x00" {
		t.Fatal("database file is not encrypted")
	}

	// Reopen with the correct passphrase.
	s2, err := Open(path, pass)
	if err != nil {
		t.Fatalf("reopen with passphrase: %v", err)
	}
	meta, err := s2.Meta()
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if meta.Mnemonic != "secret words" {
		t.Errorf("mnemonic = %q, want %q", meta.Mnemonic, "secret words")
	}
	s2.Close()

	// A wrong passphrase must fail loudly, not corrupt the file.
	if _, err := Open(path, "wrong"); err == nil {
		t.Fatal("Open with wrong passphrase succeeded, want error")
	}
}
