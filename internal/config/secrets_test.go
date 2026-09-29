package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/secretstore"
	"github.com/zalando/go-keyring"
)

type fakeSecrets map[string]error

func (f fakeSecrets) Get(id string, kind secretstore.Kind) (string, error) {
	if err, ok := f[id+"/"+string(kind)]; ok {
		return "", err
	}
	return "", secretstore.ErrNotFound
}

// fakeWriter is a secretWriter fake that fails Set for accounts named in
// failSet and records every Set/Prune call so tests can assert who was
// reached and who was skipped.
type fakeWriter struct {
	failSet     map[string]error
	setCalls    []string
	pruneCalled bool
}

func (f *fakeWriter) Set(id string, kind secretstore.Kind, _ string) error {
	f.setCalls = append(f.setCalls, id+"/"+string(kind))
	if err, ok := f.failSet[id]; ok {
		return err
	}
	return nil
}

func (f *fakeWriter) Prune(_ []string) error {
	f.pruneCalled = true
	return nil
}

func TestHydrateTreatsMissingChunkAsAbsentAndWarnsWithoutValue(t *testing.T) {
	missing := fmt.Errorf("%w: missing chunk 1 of bad/oauth: %w", secretstore.ErrUnreadable, keyring.ErrNotFound)
	cfg := Config{Providers: []Provider{{ID: "bad", OAuth: &OAuthToken{Email: "a@b.c"}}}}
	var warn bytes.Buffer
	if err := hydrateFrom(fakeSecrets{"bad/oauth": missing}, &cfg, &warn); err != nil {
		t.Fatalf("missing chunk must not fail hydrate: %v", err)
	}
	if cfg.Providers[0].HasOAuth() {
		t.Fatalf("unreadable token must read as absent: %#v", cfg.Providers[0].OAuth)
	}
	if !strings.Contains(warn.String(), `"bad"`) {
		t.Fatalf("warning must name the account: %q", warn.String())
	}
}

func TestHydrateStillFailsOnBackendError(t *testing.T) {
	denied := errors.New("security: interaction not allowed")
	cfg := Config{Providers: []Provider{{ID: "acct"}}}
	if err := hydrateFrom(fakeSecrets{"acct/apikey": denied}, &cfg, io.Discard); !errors.Is(err, denied) {
		t.Fatalf("backend failure must surface, got %v", err)
	}
}

// A corrupt stored token must cost only that account a re-login; it must not
// make Load fail, which would stop `peaproxy serve` and every CLI command.
func TestLoadTreatsCorruptOAuthSecretAsAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: good-key
    adapter: anthropic
    tier: paid
  - id: bad-oauth
    adapter: anthropic_oauth
    tier: paid
    oauth:
      email: a@b.c
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("good-key", secretstore.KindAPIKey, "sk-good"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("bad-oauth", secretstore.KindOAuth, `{"accessToken":"trunc`); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load must survive one corrupt secret: %v", err)
	}
	if cfg.Providers[0].APIKey != "sk-good" {
		t.Fatalf("healthy account lost its key: %#v", cfg.Providers[0])
	}
	if cfg.Providers[1].HasOAuth() {
		t.Fatalf("corrupt token must read as absent: %#v", cfg.Providers[1].OAuth)
	}
	if cfg.Providers[1].OAuth == nil || cfg.Providers[1].OAuth.Email != "a@b.c" {
		t.Fatalf("yaml metadata lost: %#v", cfg.Providers[1].OAuth)
	}
}

// One account's Set failure (e.g. a locked keychain) must not stop every
// later account's secret from being persisted, and a failed write must
// never lead to Prune deleting anything.
func TestPersistContinuesPastAFailingAccount(t *testing.T) {
	boom := errors.New("keychain locked")
	cfg := Config{Providers: []Provider{
		{ID: "a", OAuth: &OAuthToken{AccessToken: "tok-a"}},
		{ID: "b", OAuth: &OAuthToken{AccessToken: "tok-b"}},
		{ID: "c", OAuth: &OAuthToken{AccessToken: "tok-c"}},
	}}
	fw := &fakeWriter{failSet: map[string]error{"a": boom}}
	_, err := persistTo(fw, cfg)
	if err == nil {
		t.Fatal("expected an error when account \"a\"'s Set fails")
	}
	for _, want := range []string{"b/oauth", "c/oauth"} {
		if !slices.Contains(fw.setCalls, want) {
			t.Fatalf("Set was not attempted for %s despite account \"a\"'s failure: %v", want, fw.setCalls)
		}
	}
	if !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf("error must name the failing account: %v", err)
	}
	if fw.pruneCalled {
		t.Fatal("Prune must not run when a write failed")
	}
}
