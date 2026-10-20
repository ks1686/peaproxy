package secretstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func withKeyringOpener(t *testing.T, fn func(string) (*Store, error)) {
	t.Helper()
	old := openKeyringFn
	openKeyringFn = fn
	t.Cleanup(func() { openKeyringFn = old })
}

// The backend was re-chosen on every Open. One failed keychain probe (a locked
// login keychain) sent a rotated refresh token to secrets.enc while the
// keychain kept the stale one, and the next load read the stale one back.
func TestAKeychainConfigDoesNotFallBackToTheFileOnAFailedProbe(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, IndexFileName), []byte(`["a/oauth"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	withKeyringOpener(t, func(string) (*Store, error) { return nil, errors.New("User interaction is not allowed") })
	if s, err := openAuto(dir, ""); err == nil {
		t.Fatalf("opened the %v backend for a config whose secrets are in the keychain", s.Backend())
	}
}

// The reverse split: secrets written to the file during an earlier keychain
// outage must keep being read from the file once the keychain answers again.
func TestAFileConfigStaysOnTheFileWhenTheKeychainWorks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, EncryptedFileName), []byte(Magic+"x"), 0o600); err != nil {
		t.Fatal(err)
	}
	withKeyringOpener(t, func(d string) (*Store, error) { return &Store{backend: BackendKeyring, dir: d}, nil })
	s, err := openAuto(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() != BackendFile {
		t.Fatalf("backend = %v, want the file that holds this config's secrets", s.Backend())
	}
}

func TestAFreshConfigPrefersTheKeychainAndFallsBackToTheFile(t *testing.T) {
	withKeyringOpener(t, func(d string) (*Store, error) { return &Store{backend: BackendKeyring, dir: d}, nil })
	if s, err := openAuto(t.TempDir(), ""); err != nil || s.Backend() != BackendKeyring {
		t.Fatalf("fresh dir with a working keychain: %v, %v", s, err)
	}
	withKeyringOpener(t, func(string) (*Store, error) { return nil, errors.New("no keychain") })
	if s, err := openAuto(t.TempDir(), ""); err != nil || s.Backend() != BackendFile {
		t.Fatalf("fresh dir without a keychain: %v, %v", s, err)
	}
}

// Prune writes an index even when the keychain holds nothing, so "[]" proves
// nothing about where this config's secrets are.
func TestAnEmptyKeychainIndexDoesNotPinTheKeychain(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, IndexFileName), []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, EncryptedFileName), []byte(Magic+"x"), 0o600); err != nil {
		t.Fatal(err)
	}
	withKeyringOpener(t, func(string) (*Store, error) { return nil, errors.New("locked") })
	s, err := openAuto(dir, "")
	if err != nil || s.Backend() != BackendFile {
		t.Fatalf("empty index with a file store: %v, %v; want the file backend", s, err)
	}
}
