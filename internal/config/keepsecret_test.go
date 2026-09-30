package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/secretstore"
)

// #54: the server keeps its in-memory secret for an account it did not touch,
// and only when the copy on disk is unreadable. Every other "disk has no
// secret" means the user removed it on purpose, and must stay removed.

type seqReader struct {
	vals map[string]string
	errs map[string]error
}

func (s *seqReader) Get(id string, kind secretKind) (string, error) {
	k := id + "/" + string(kind)
	if err, ok := s.errs[k]; ok {
		return "", err
	}
	if v, ok := s.vals[k]; ok {
		return v, nil
	}
	return "", secretstore.ErrNotFound
}

func TestKeepSecretsOnlyForSecretsThatWereUnreadable(t *testing.T) {
	tok := OAuthToken{AccessToken: "live", RefreshToken: "r", ExpiresAt: "2030-01-01T00:00:00Z"}

	t.Run("unreadable on disk keeps the live token", func(t *testing.T) {
		disk := Provider{ID: "acct", OAuth: &OAuthToken{AccountID: "acct"}}
		mine := Provider{ID: "acct", OAuth: &tok}
		got := keepSecrets(disk, mine, unreadableSecrets{{"acct", secretOAuth}})
		if got.OAuth == nil || got.OAuth.AccessToken != "live" {
			t.Fatalf("live token was dropped: %+v", got.OAuth)
		}
		// The public fields still come from disk.
		if got.OAuth.AccountID != "acct" {
			t.Errorf("public field overwritten: %+v", got.OAuth)
		}
	})

	t.Run("no oauth block at all means removed on purpose", func(t *testing.T) {
		disk := Provider{ID: "acct"}
		mine := Provider{ID: "acct", OAuth: &tok}
		got := keepSecrets(disk, mine, nil)
		if oauthHasSecret(got.OAuth) {
			t.Fatalf("a deleted oauth block came back from memory: %+v", got.OAuth)
		}
	})

	t.Run("no key in the store means removed on purpose", func(t *testing.T) {
		disk := Provider{ID: "acct", OAuth: &OAuthToken{AccountID: "acct"}}
		mine := Provider{ID: "acct", OAuth: &tok}
		got := keepSecrets(disk, mine, nil)
		if oauthHasSecret(got.OAuth) {
			t.Fatalf("a deleted token came back from memory: %+v", got.OAuth)
		}
	})

	t.Run("a key that was switched to apiKeyEnv stays gone", func(t *testing.T) {
		disk := Provider{ID: "acct", APIKey: "", OAuth: nil, APIKeyEnv: "MY_KEY"}
		mine := Provider{ID: "acct", APIKey: "sk-old", OAuth: &tok}
		got := keepSecrets(disk, mine, nil)
		if got.APIKey != "" {
			t.Errorf("an api key the user replaced with an env var came back: %q", got.APIKey)
		}
		if oauthHasSecret(got.OAuth) {
			t.Errorf("the token came back too: %+v", got.OAuth)
		}
	})

	t.Run("unreadable api key keeps the live one", func(t *testing.T) {
		disk := Provider{ID: "acct"}
		mine := Provider{ID: "acct", APIKey: "sk-live"}
		got := keepSecrets(disk, mine, unreadableSecrets{{"acct", secretAPIKey}})
		if got.APIKey != "sk-live" {
			t.Errorf("live api key dropped: %q", got.APIKey)
		}
	})
}

// hydrateFrom has to say which secrets it could not read. That is the only
// thing that licenses the merge to resurrect one.
func TestHydrateFromReportsWhatWasUnreadable(t *testing.T) {
	store := &seqReader{
		vals: map[string]string{
			"good/apikey":   "sk-good",
			"good/oauth":    `{"accessToken":"t"}`,
			"gone/apikey":   "", // replaced below
			"corrupt/oauth": "{not json",
		},
		errs: map[string]error{
			"broken/apikey": secretstore.ErrUnreadable,
		},
	}
	delete(store.vals, "gone/apikey")
	cfg := Config{Providers: []Provider{
		{ID: "good"},
		{ID: "gone"},
		{ID: "broken"},
		{ID: "corrupt"},
		{ID: "never"},
	}}
	unreadable, err := hydrateFromReporting(store, &cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := unreadableSecrets{
		{"broken", secretAPIKey},
		{"corrupt", secretOAuth},
	}
	if len(unreadable) != len(want) {
		t.Fatalf("reported %v, want %v", unreadable, want)
	}
	for _, w := range want {
		if !unreadable.has(w) {
			t.Errorf("%v was not reported as unreadable", w)
		}
	}
	// Not-found is not unreadable. That distinction is the whole fix.
	for _, notUnreadable := range [][2]string{{"gone", "apikey"}, {"never", "apikey"}, {"never", "oauth"}, {"good", "apikey"}} {
		if unreadable.has(secretRef{notUnreadable[0], secretKind(notUnreadable[1])}) {
			t.Errorf("%v/%v was reported unreadable but was simply absent", notUnreadable[0], notUnreadable[1])
		}
	}
}

// End to end: the user deletes an account's key from the store and the YAML,
// and the next server save must not put it back.
func TestDeletedSecretIsNotResurrectedByAServerSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	body := `schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: acct
    adapter: openai_key
    baseURL: https://api.openai.com
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// What the server believes in memory: a key the user has since removed.
	mine := Config{SchemaVersion: SchemaVersion, Bind: "127.0.0.1", Port: DefaultPort}
	mine.Providers = []Provider{{ID: "acct", Adapter: "openai_key", BaseURL: "https://api.openai.com", APIKey: "sk-removed"}}
	base := Config{SchemaVersion: SchemaVersion, Bind: "127.0.0.1", Port: DefaultPort}
	base.Providers = []Provider{{ID: "acct", Adapter: "openai_key", BaseURL: "https://api.openai.com", APIKey: "sk-removed"}}

	merged, err := SaveMerged(path, base, mine)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Providers) != 1 || merged.Providers[0].APIKey != "" {
		t.Fatalf("the removed key came back: %+v", merged.Providers)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-removed") {
		t.Fatalf("the removed key was written back to disk:\n%s", raw)
	}
}
