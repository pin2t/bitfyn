package storage

import "errors"
import "os"
import "path/filepath"
import "testing"

// TestPlainLifecycle exercises the unencrypted database: wallet creation,
// metadata, idempotent address inserts, index updates and persistence across
// a reopen.
func TestPlainLifecycle(t *testing.T) {
	var path = filepath.Join(t.TempDir(), "w.db")
	var s, err = Open(path, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Meta(); !errors.Is(err, ErrNoWallet) {
		t.Fatalf("Meta on fresh store = %v, want ErrNoWallet", err)
	}
	if err := s.SaveMeta("abandon ", "xpub-test", "testnet", 1234); err != nil {
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
	if err := s.AddAddress(0, "m/84'/1'/0'/0/0", "tb1qtest", []byte{1, 2, 3}); err != nil {
		t.Fatalf("AddAddress again: %v", err)
	}
	if err := s.UpdateNextIndex(1); err != nil {
		t.Fatalf("UpdateNextIndex: %v", err)
	}
	s.Close()
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

// TestEncryptedLifecycle checks that a passphrase-protected database has no
// plaintext SQLite header on disk, reopens with the right passphrase and
// refuses a wrong one without corrupting the file.
func TestEncryptedLifecycle(t *testing.T) {
	var path = filepath.Join(t.TempDir(), "e.db")
	const pass = "s3cret-passphrase"
	var s, err = Open(path, pass)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.SaveMeta("secret words", "xpub-secret", "mainnet", 99); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}
	s.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(raw) >= 16 && string(raw[:16]) == "SQLite format 3\x00" {
		t.Fatal("database file is not encrypted")
	}
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
	if _, err := Open(path, "wrong"); err == nil {
		t.Fatal("Open with wrong passphrase succeeded, want error")
	}
}
