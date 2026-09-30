package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/secretstore"
)

// copilotToken is the shape the Copilot adapter saves: the GitHub token is
// mirrored into RefreshToken and also kept in Extra, because it is what mints
// Copilot sessions.
func copilotToken(github string) *OAuthToken {
	return &OAuthToken{
		AccessToken:  "sess-1",
		RefreshToken: github,
		ExpiresAt:    "2030-01-01T00:00:00Z",
		Email:        "dev@example.com",
		Extra:        map[string]string{"github_token": github, "copilot_plan": "individual"},
	}
}

// #72: the GitHub access token can mint Copilot sessions, so it must not be
// written to config.yaml in the clear.
func TestGitHubTokenIsNotWrittenToYAML(t *testing.T) {
	const github = "gho_live_0123456789abcdef"
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	cfg := Config{SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317, Providers: []Provider{
		{ID: "copilot", Adapter: "copilot_oauth", OAuth: copilotToken(github)},
	}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), github) {
		t.Fatalf("the GitHub token was written to %s:\n%s", path, raw)
	}
	// The non-secret half of Extra is still there: this is a filter, not a
	// blanket drop of everything the account carried.
	if !strings.Contains(string(raw), "individual") {
		t.Fatalf("non-secret Extra was dropped too:\n%s", raw)
	}
}

// ...and it must still come back, or every restart would need a fresh login.
func TestGitHubTokenSurvivesRestart(t *testing.T) {
	const github = "gho_live_0123456789abcdef"
	dir := t.TempDir()
	t.Setenv("PEAPROXY_SECRET_BACKEND", "file")
	path := filepath.Join(dir, "peaproxy.yaml")
	cfg := Config{SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317, Providers: []Provider{
		{ID: "copilot", Adapter: "copilot_oauth", OAuth: copilotToken(github)},
	}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Providers[0].OAuth
	if got == nil {
		t.Fatal("token lost on load")
	}
	if got.RefreshToken != github {
		t.Fatalf("refresh token = %q, want it back from the secret store", got.RefreshToken)
	}
	if got.Extra["github_token"] != github {
		t.Fatalf("Extra github_token = %q, want it back from the secret store", got.Extra["github_token"])
	}
}

// An account whose only secret is a secret-named Extra key must still be sent
// to the store, not treated as having nothing to protect.
func TestSecretExtraKeyAloneCountsAsASecret(t *testing.T) {
	if !oauthHasSecret(&OAuthToken{Extra: map[string]string{"github_token": "gho_x"}}) {
		t.Fatal("an account holding only a GitHub token is not recognised as needing the secret store")
	}
	if oauthHasSecret(&OAuthToken{Extra: map[string]string{"copilot_plan": "individual"}}) {
		t.Fatal("a token with only non-secret Extra should not be sent to the store")
	}
}

// The rule is the one place that decides what leaves the secret store, so it
// gets its own table.
func TestExtraKeyIsSecret(t *testing.T) {
	for _, k := range []string{
		"access_token", "refresh_token", "id_token", "api_key", "password",
		"secret", "dca_token", "github_token", "GitHub_Token", " github_token ",
	} {
		if !extraKeyIsSecret(k) {
			t.Errorf("%q should be treated as a secret", k)
		}
	}
	for _, k := range []string{"copilot_plan", "account_id", "email", "device_id", ""} {
		if extraKeyIsSecret(k) {
			t.Errorf("%q should not be treated as a secret", k)
		}
	}
}

// publicOAuth is what lands in the YAML copy.
func TestPublicOAuthDropsSecretExtraKeys(t *testing.T) {
	out := publicOAuth(copilotToken("gho_live_x"))
	if _, ok := out.Extra["github_token"]; ok {
		t.Fatalf("github_token survived publicOAuth: %#v", out.Extra)
	}
	if out.Extra["copilot_plan"] != "individual" {
		t.Fatalf("non-secret Extra lost: %#v", out.Extra)
	}
	if out.AccessToken != "" || out.RefreshToken != "" {
		t.Fatalf("tokens survived publicOAuth: %#v", out)
	}
	if out.Email != "dev@example.com" {
		t.Fatalf("identity metadata lost: %#v", out)
	}
}

// A token whose Extra is all secret still leaves an entry the hydration step
// can merge the stored token onto, but the entry itself must be empty.
func TestPublicOAuthLeavesAnEmptyShellNotSecrets(t *testing.T) {
	got := publicOAuth(&OAuthToken{Extra: map[string]string{"github_token": "gho_x"}})
	if got == nil {
		t.Fatal("expected an entry for hydration to merge onto")
	}
	if got.AccessToken != "" || got.RefreshToken != "" || len(got.Extra) != 0 {
		t.Fatalf("secrets survived into the yaml copy: %#v", got)
	}
}

// The secret store copy must be the complete token, Extra included.
func TestSecretStoreCopyKeepsEveryExtraKey(t *testing.T) {
	fw := &fakeWriter{}
	cfg := Config{Providers: []Provider{{ID: "copilot", OAuth: copilotToken("gho_live_x")}}}
	if _, err := persistTo(fw, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(fw.setCalls, ","), "copilot/oauth") {
		t.Fatalf("the token was not sent to the store: %v", fw.setCalls)
	}
	// persistTo hands the marshalled full token to Set; assert on the shape it
	// marshals rather than on the fake's discarded argument.
	raw, err := json.Marshal(copilotToken("gho_live_x"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "github_token") {
		t.Fatalf("the stored form lost the GitHub token: %s", raw)
	}
	if secretstore.KindOAuth != "oauth" {
		t.Fatalf("unexpected secret kind %q", secretstore.KindOAuth)
	}
}
