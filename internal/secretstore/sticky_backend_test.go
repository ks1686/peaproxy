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

func TestADecryptableFileBlobWithItemsStillPinsTheFile(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("acct", KindOAuth, oauthJSON("file-tok")); err != nil {
		t.Fatal(err)
	}
	withKeyringOpener(t, func(d string) (*Store, error) { return &Store{backend: BackendKeyring, dir: d}, nil })
	opened, err := openAuto(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Backend() != BackendFile {
		t.Fatalf("backend = %v, want the file that holds this config's secrets", opened.Backend())
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

// A scoped `[]` index is not evidence this config's secrets live in the
// keychain, so backend selection still consults the legacy index it will
// migrate from.
func TestAnEmptyScopedIndexFallsBackToTheLegacyIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.config.yaml.index"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, IndexFileName), []byte(`["anthropic-oauth/oauth"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	withKeyringOpener(t, func(string) (*Store, error) { return nil, errors.New("User interaction is not allowed") })
	if s, err := openAuto(dir, "config.yaml"); err == nil {
		t.Fatalf("opened the %v backend; the legacy index says secrets are in the keychain", s.Backend())
	}
}

// After a finished migrate and a prune of every secret, the scoped index is
// `[]` and the never-pruned legacy index still names keys. The migrated
// marker must keep backend selection off that legacy evidence, or a failed
// keychain probe refuses to open a scope that holds nothing.
func TestAMigratedEmptyScopeDoesNotPinTheLegacyKeychain(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.config.yaml.index"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.config.yaml.migrated"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, IndexFileName), []byte(`["anthropic-oauth/oauth"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	withKeyringOpener(t, func(string) (*Store, error) { return nil, errors.New("User interaction is not allowed") })
	s, err := openAuto(dir, "config.yaml")
	if err != nil || s.Backend() != BackendFile {
		t.Fatalf("migrated empty scope: %v, %v; want the file backend", s, err)
	}
}

// writeVacuousEnc writes a decryptable secrets.<scope>.enc with 0 items and
// size > 0, the Mac 3.3.3 layout that used to pin the file backend.
func writeVacuousEnc(t *testing.T, dir, scope string) {
	t.Helper()
	s := &Store{backend: BackendFile, dir: dir, scope: scope}
	if err := s.saveBlob(fileBlob{V: 1, Items: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if !nonEmptyFile(s.encPath()) {
		t.Fatal("setup: vacuous enc was not written")
	}
	blob, err := s.loadBlobExisting()
	if err != nil || len(blob.Items) != 0 {
		t.Fatalf("setup: blob items = %v, %v; want 0 items", blob.Items, err)
	}
}

// 3.3.3: empty `[]` index is ignored, but a leftover enc whose size is > 0
// even though it decrypts to 0 items still pinned the file backend, so a
// LaunchAgent auto open never read Keychain.
func TestAVacuousEncDoesNotPinTheFileWhenTheKeychainWorks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.config.yaml.index"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.config.yaml.migrated"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeVacuousEnc(t, dir, "config.yaml")
	withKeyringOpener(t, func(d string) (*Store, error) { return &Store{backend: BackendKeyring, dir: d}, nil })
	s, err := openAuto(dir, "config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() != BackendKeyring {
		t.Fatalf("backend = %v, want keyring; a 0-item enc must not pin the file", s.Backend())
	}
}

// Same leftover blob, keychain unavailable: still the file fallback, as for
// a fresh directory. The vacuous enc is not itself a file-primary store.
func TestAVacuousEncFallsBackToTheFileWhenTheKeychainIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.config.yaml.index"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeVacuousEnc(t, dir, "config.yaml")
	withKeyringOpener(t, func(string) (*Store, error) { return nil, errors.New("User interaction is not allowed") })
	s, err := openAuto(dir, "config.yaml")
	if err != nil || s.Backend() != BackendFile {
		t.Fatalf("vacuous enc without a keychain: %v, %v; want the file backend", s, err)
	}
}

// A vacuous scoped enc must not count as "already migrated", or openAuto
// ignores the legacy keychain index it still has to copy from.
func TestAVacuousScopedEncStillConsultsTheLegacyKeychainIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.config.yaml.index"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeVacuousEnc(t, dir, "config.yaml")
	if err := os.WriteFile(filepath.Join(dir, IndexFileName), []byte(`["anthropic-oauth/oauth"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	withKeyringOpener(t, func(string) (*Store, error) { return nil, errors.New("User interaction is not allowed") })
	if s, err := openAuto(dir, "config.yaml"); err == nil {
		t.Fatalf("opened the %v backend; the legacy index says secrets are in the keychain", s.Backend())
	}
}

// Mac 3.3.3 LaunchAgent: empty scoped index, decryptable 0-item enc, secrets
// already in Keychain. Auto must open the keychain, surface those secrets, and
// keep a new OAuth persist visible on the next auto open (index records it).
func TestAVacuousEncDoesNotHideKeychainSecretsFromAutoOpen(t *testing.T) {
	dir := t.TempDir()
	kr := &memKeyring{m: map[string]string{}}
	seed := scopedKeyring(t, dir, "config.yaml", kr)
	if err := seed.Set("anthropic-oauth", KindOAuth, oauthJSON("claude-tok")); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "secrets.config.yaml.index"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.config.yaml.migrated"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeVacuousEnc(t, dir, "config.yaml")

	withKeyringOpener(t, func(d string) (*Store, error) {
		return &Store{backend: BackendKeyring, dir: d, kr: kr}, nil
	})
	opened, err := openAuto(dir, "config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Backend() != BackendKeyring {
		t.Fatalf("backend = %v, want keyring", opened.Backend())
	}
	if err := opened.scopeTo("config.yaml"); err != nil {
		t.Fatal(err)
	}
	got, err := opened.Get("anthropic-oauth", KindOAuth)
	if err != nil || accessToken(t, got) != "claude-tok" {
		t.Fatalf("keychain secret after auto open: %q, %v", got, err)
	}

	if err := opened.Set("xai-oauth", KindOAuth, oauthJSON("xai-tok")); err != nil {
		t.Fatal(err)
	}
	if !keyringIndexHasKeys(opened.indexPath()) {
		t.Fatal("Set did not record the new OAuth secret in the index")
	}

	again, err := openAuto(dir, "config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if again.Backend() != BackendKeyring {
		t.Fatalf("reopen backend = %v, want keyring after the new Set", again.Backend())
	}
	if err := again.scopeTo("config.yaml"); err != nil {
		t.Fatal(err)
	}
	got, err = again.Get("xai-oauth", KindOAuth)
	if err != nil || accessToken(t, got) != "xai-tok" {
		t.Fatalf("new OAuth secret after auto reopen: %q, %v", got, err)
	}
	got, err = again.Get("anthropic-oauth", KindOAuth)
	if err != nil || accessToken(t, got) != "claude-tok" {
		t.Fatalf("existing keychain secret after auto reopen: %q, %v", got, err)
	}
}
