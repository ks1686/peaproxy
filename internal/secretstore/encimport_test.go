package secretstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
)

func oauthJSON(access string) string {
	return `{"accessToken":"` + access + `"}`
}

func accessToken(t *testing.T, raw string) string {
	t.Helper()
	var tok struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal([]byte(raw), &tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("secret %q is not an oauth token HasOAuth would accept: %v", raw, err)
	}
	return tok.AccessToken
}

func writeLegacyEnc(t *testing.T, dir, id, value string) {
	t.Helper()
	s, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(id, KindOAuth, value); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyKeyring(t *testing.T, dir string, kr keyringAPI, id, value string) *Store {
	t.Helper()
	legacy := &Store{backend: BackendKeyring, dir: dir, kr: kr}
	if err := legacy.Set(id, KindOAuth, value); err != nil {
		t.Fatal(err)
	}
	return legacy
}

// A machine that stored some accounts in secrets.enc and others in the
// keychain used to migrate only the keychain index, then stick to keyring
// forever, so Antigravity/OpenAI stayed oauth-login-required while Claude
// became oauth-ready.
func TestKeyringMigrateImportsLegacyEncItemsAbsentFromTheKeyringIndex(t *testing.T) {
	dir := t.TempDir()
	kr := &memKeyring{m: map[string]string{}}
	writeLegacyEnc(t, dir, "antigravity", oauthJSON("ag-tok"))
	writeLegacyEnc(t, dir, "openai-oauth", oauthJSON("oai-tok"))
	legacyKR := writeLegacyKeyring(t, dir, kr, "anthropic-oauth", oauthJSON("claude-tok"))

	s := scopedKeyring(t, dir, "config.yaml", kr)
	for id, want := range map[string]string{
		"anthropic-oauth": "claude-tok",
		"antigravity":     "ag-tok",
		"openai-oauth":    "oai-tok",
	} {
		got, err := s.Get(id, KindOAuth)
		if err != nil {
			t.Fatalf("%s after migrate: %v", id, err)
		}
		if accessToken(t, got) != want {
			t.Fatalf("%s after migrate: %q, want %s", id, got, want)
		}
	}

	if got, err := legacyKR.Get("anthropic-oauth", KindOAuth); err != nil || accessToken(t, got) != "claude-tok" {
		t.Fatalf("legacy keychain item was rewritten: %q, %v", got, err)
	}
	legacyFile, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := legacyFile.Get("antigravity", KindOAuth); err != nil || accessToken(t, got) != "ag-tok" {
		t.Fatalf("legacy secrets.enc was rewritten: %q, %v", got, err)
	}
	if got, err := legacyFile.Get("openai-oauth", KindOAuth); err != nil || accessToken(t, got) != "oai-tok" {
		t.Fatalf("legacy secrets.enc openai was rewritten: %q, %v", got, err)
	}
}

// 3.3.2 copied Claude from the keychain index, wrote secrets.<config>.migrated,
// and never looked at secrets.enc again. Opening that layout must still make
// the enc-only accounts gettable (HasOAuth true once hydrated).
func TestEncOnlyAccountsAreGettableAfterAPartialKeyringMigrate(t *testing.T) {
	dir := t.TempDir()
	kr := &memKeyring{m: map[string]string{}}
	writeLegacyKeyring(t, dir, kr, "anthropic-oauth", oauthJSON("claude-tok"))
	s := scopedKeyring(t, dir, "config.yaml", kr)
	if _, err := os.Stat(s.migratedPath()); err != nil {
		t.Fatalf("setup: no migrated marker: %v", err)
	}
	if got, err := s.Get("anthropic-oauth", KindOAuth); err != nil || accessToken(t, got) != "claude-tok" {
		t.Fatalf("setup: claude %q, %v", got, err)
	}
	writeLegacyEnc(t, dir, "antigravity", oauthJSON("ag-tok"))
	writeLegacyEnc(t, dir, "openai-oauth", oauthJSON("oai-tok"))

	s = scopedKeyring(t, dir, "config.yaml", kr)
	for id, want := range map[string]string{
		"anthropic-oauth": "claude-tok",
		"antigravity":     "ag-tok",
		"openai-oauth":    "oai-tok",
	} {
		got, err := s.Get(id, KindOAuth)
		if err != nil {
			t.Fatalf("%s after reopen: %v (HasOAuth would be false)", id, err)
		}
		if accessToken(t, got) != want {
			t.Fatalf("%s after reopen: %q, want %s", id, got, want)
		}
	}
}

// A prune must not resurrect enc-only accounts from the leftover blob.
func TestPrunedEncOnlyAccountsAreNotResurrectedFromTheLeftoverBlob(t *testing.T) {
	dir := t.TempDir()
	kr := &memKeyring{m: map[string]string{}}
	writeLegacyEnc(t, dir, "antigravity", oauthJSON("ag-tok"))
	writeLegacyKeyring(t, dir, kr, "anthropic-oauth", oauthJSON("claude-tok"))
	s := scopedKeyring(t, dir, "config.yaml", kr)
	if err := s.Prune([]string{"anthropic-oauth"}); err != nil {
		t.Fatal(err)
	}
	s = scopedKeyring(t, dir, "config.yaml", kr)
	if _, err := s.Get("antigravity", KindOAuth); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pruned enc-only account came back: %v", err)
	}
	legacyFile, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := legacyFile.Get("antigravity", KindOAuth); err != nil || accessToken(t, got) != "ag-tok" {
		t.Fatalf("legacy secrets.enc was rewritten: %q, %v", got, err)
	}
}

// File migrate copies leftover secrets.enc (never moved, never pruned). After
// the last scoped secret is deleted, this PR's vacuous-enc openAuto picks
// keyring; import must not copy that leftover back into Keychain.
func TestFileMigrateThenPruneDoesNotResurrectFromLegacyEncOnKeyringOpen(t *testing.T) {
	dir := t.TempDir()
	writeLegacyEnc(t, dir, "acct", oauthJSON("old-tok"))
	file := scopedFile(t, dir, "config.yaml")
	if got, err := file.Get("acct", KindOAuth); err != nil || accessToken(t, got) != "old-tok" {
		t.Fatalf("setup file migrate: %q, %v", got, err)
	}
	if _, err := os.Stat(file.migratedPath()); err != nil {
		t.Fatalf("setup: no migrated marker: %v", err)
	}
	if err := file.Prune(nil); err != nil {
		t.Fatal(err)
	}
	if fileBlobHasItems(dir, "config.yaml") {
		t.Fatal("setup: scoped enc still has items after prune")
	}
	legacy, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := legacy.Get("acct", KindOAuth); err != nil || accessToken(t, got) != "old-tok" {
		t.Fatalf("legacy secrets.enc was rewritten: %q, %v", got, err)
	}

	kr := &memKeyring{m: map[string]string{}}
	s := scopedKeyring(t, dir, "config.yaml", kr)
	if _, err := s.Get("acct", KindOAuth); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pruned secret resurrected from leftover secrets.enc: %v", err)
	}
}

// 3.3.3 file migrate wrote .migrated but not .imported. The same prune then
// keyring open must still not resurrect, not only the path that recorded
// leftover keys at migrate time.
func TestFileMigrateWithoutImportedDoesNotResurrectAfterPrune(t *testing.T) {
	dir := t.TempDir()
	writeLegacyEnc(t, dir, "acct", oauthJSON("old-tok"))
	file := scopedFile(t, dir, "config.yaml")
	if err := os.Remove(file.importedPath()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := file.Prune(nil); err != nil {
		t.Fatal(err)
	}
	legacy, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := legacy.Get("acct", KindOAuth); err != nil || accessToken(t, got) != "old-tok" {
		t.Fatalf("legacy secrets.enc was rewritten: %q, %v", got, err)
	}

	kr := &memKeyring{m: map[string]string{}}
	s := scopedKeyring(t, dir, "config.yaml", kr)
	if _, err := s.Get("acct", KindOAuth); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pruned secret resurrected without .imported: %v", err)
	}
}

type failSetKeyring struct {
	memKeyring
	err error
}

func (f *failSetKeyring) Set(string, string, string) error {
	if f.err != nil {
		return f.err
	}
	return errors.New("User interaction is not allowed")
}

func TestKeyringSetFallsBackToTheEncryptedFile(t *testing.T) {
	dir := t.TempDir()
	kr := &failSetKeyring{memKeyring: memKeyring{m: map[string]string{}}, err: errors.New("User interaction is not allowed")}
	s := &Store{backend: BackendKeyring, dir: dir, kr: kr}
	if err := s.scopeTo("config.yaml"); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	tok := oauthJSON("ui-tok")
	if err := s.Set("openai-oauth", KindOAuth, tok); err != nil {
		t.Fatalf("Set with a failing keychain: %v", err)
	}
	got, err := s.Get("openai-oauth", KindOAuth)
	if err != nil || accessToken(t, got) != "ui-tok" {
		t.Fatalf("Get after keychain Set failure: %q, %v (HasOAuth would be false)", got, err)
	}
	if !nonEmptyFile(s.encPath()) {
		t.Fatal("expected the encrypted file fallback to be written")
	}
	raw, err := os.ReadFile(s.encPath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("ui-tok")) {
		t.Fatal("fallback wrote the token in plaintext")
	}
	warn := logs.String()
	if !strings.Contains(warn, "saved it in the encrypted file instead") {
		t.Fatalf("missing fallback warning: %q", warn)
	}
	if !strings.Contains(warn, "openai-oauth/oauth") {
		t.Fatalf("warning must name the secret: %q", warn)
	}
}

// A refused overwrite must not report success: Get only reads the file on
// ErrNotFound, so falling back while the keychain still holds the old item
// would leave the old token live.
func TestKeyringSetFallsBackOnlyWhenTheKeychainItemIsMissing(t *testing.T) {
	dir := t.TempDir()
	mem := memKeyring{m: map[string]string{}}
	s := &Store{backend: BackendKeyring, dir: dir, kr: &mem}
	if err := s.scopeTo("config.yaml"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("openai-oauth", KindOAuth, oauthJSON("old-tok")); err != nil {
		t.Fatal(err)
	}
	s.kr = &failSetKeyring{memKeyring: mem, err: errors.New("User interaction is not allowed")}
	if err := s.Set("openai-oauth", KindOAuth, oauthJSON("new-tok")); err == nil {
		t.Fatal("Set reported success for a refused overwrite")
	}
	got, err := s.Get("openai-oauth", KindOAuth)
	if err != nil || accessToken(t, got) != "old-tok" {
		t.Fatalf("Get after refused overwrite: %q, %v; want the old keychain token", got, err)
	}
}

type unreadableGetKeyring struct {
	failSetKeyring
}

func (u *unreadableGetKeyring) Get(string, string) (string, error) {
	return "", ErrUnreadable
}

func TestKeyringSetDoesNotFallBackOnUnreadableGet(t *testing.T) {
	dir := t.TempDir()
	s := &Store{backend: BackendKeyring, dir: dir, kr: &unreadableGetKeyring{failSetKeyring: failSetKeyring{memKeyring: memKeyring{m: map[string]string{}}, err: errors.New("User interaction is not allowed")}}}
	if err := s.scopeTo("config.yaml"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("openai-oauth", KindOAuth, oauthJSON("ui-tok")); err == nil {
		t.Fatal("Set reported success when Get was ErrUnreadable, not ErrNotFound")
	}
	if nonEmptyFile(s.encPath()) {
		t.Fatal("fallback wrote the encrypted file for an unreadable keychain item")
	}
}

// A leftover undecryptable scoped enc must not fail a keyring Get/Prune, and
// a miss must not create secret.key.
func TestKeyringGetTreatsAnUnreadableFallbackBlobAsAbsent(t *testing.T) {
	dir := t.TempDir()
	kr := &memKeyring{m: map[string]string{}}
	s := scopedKeyring(t, dir, "config.yaml", kr)
	if err := s.Set("anthropic-oauth", KindOAuth, oauthJSON("claude-tok")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.encPath(), []byte(Magic+"garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.keyPath()); !os.IsNotExist(err) {
		t.Fatal("setup: secret.key already exists")
	}

	if got, err := s.Get("anthropic-oauth", KindOAuth); err != nil || accessToken(t, got) != "claude-tok" {
		t.Fatalf("keychain account: %q, %v", got, err)
	}
	_, err := s.Get("antigravity", KindOAuth)
	if err == nil || (!errors.Is(err, ErrNotFound) && !errors.Is(err, ErrUnreadable)) {
		t.Fatalf("missing account with a corrupt fallback blob: %v", err)
	}
	if _, err := os.Stat(s.keyPath()); !os.IsNotExist(err) {
		t.Fatal("Get created secret.key while reading an unreadable fallback blob")
	}
	if err := s.Prune([]string{"anthropic-oauth"}); err != nil {
		t.Fatalf("Prune with an unreadable fallback blob: %v", err)
	}
	if err := s.Delete("antigravity", KindOAuth); err != nil {
		t.Fatalf("Delete with an unreadable fallback blob: %v", err)
	}

	_ = os.Remove(s.encPath())
	file := &Store{backend: BackendFile, dir: dir, scope: s.scope}
	if err := file.Set("dummy", KindAPIKey, "x"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.encPath())
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(s.encPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = s.Get("antigravity", KindOAuth)
	if err == nil || (!errors.Is(err, ErrNotFound) && !errors.Is(err, ErrUnreadable)) {
		t.Fatalf("missing account with a tampered fallback blob: %v", err)
	}
	if err := s.Prune([]string{"anthropic-oauth"}); err != nil {
		t.Fatalf("Prune with a tampered fallback blob: %v", err)
	}
}
