package copilot_oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/oauth"
)

// #72: ensureToken used to set next.RefreshToken to the GitHub token it had just
// spent, unconditionally. When the provider rotates the refresh token, that
// threw the new one away and persisted the spent one, so the next refresh would
// present a token the provider had already invalidated.
func TestRefreshKeepsRotatedRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":         "tid=fresh",
			"expires_at":    time.Now().Add(time.Hour).Unix(),
			"refresh_token": "ghu_rotated",
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv)
	a.token = oauth.Token{RefreshToken: "ghu_old", ExpiresAt: time.Now().Add(-time.Minute)}
	if err := a.ensureToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.token.RefreshToken != "ghu_rotated" {
		t.Fatalf("refresh token = %q, want the rotated one", a.token.RefreshToken)
	}
}

// A response that omits the refresh token still falls back to the one we have,
// which is the case that made the old unconditional assignment look safe.
func TestRefreshFallsBackWhenNoRefreshTokenReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "tid=fresh",
			"expires_at": time.Now().Add(time.Hour).Unix(),
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv)
	a.token = oauth.Token{RefreshToken: "ghu_old", ExpiresAt: time.Now().Add(-time.Minute)}
	if err := a.ensureToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.token.RefreshToken != "ghu_old" {
		t.Fatalf("refresh token = %q, want the fallback", a.token.RefreshToken)
	}
}

// The GitHub token also lives in Extra for tokens saved before it was mirrored
// into RefreshToken, and a refresh has to find it there.
func TestRefreshUsesGitHubTokenFromExtra(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "tid=fresh",
			"expires_at": time.Now().Add(time.Hour).Unix(),
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv)
	a.token = oauth.Token{
		AccessToken: "stale",
		ExpiresAt:   time.Now().Add(-time.Minute),
		Extra:       map[string]string{githubTokenExtra: "ghu_from_extra"},
	}
	if err := a.ensureToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer ghu_from_extra" {
		t.Fatalf("refresh auth = %q, want the token from Extra", auth)
	}
	if a.token.RefreshToken != "ghu_from_extra" {
		t.Fatalf("refresh token = %q, want the token from Extra", a.token.RefreshToken)
	}
	// KeepExtra carries it forward, so the next refresh still has it.
	if a.token.ExtraGet(githubTokenExtra) != "ghu_from_extra" {
		t.Fatalf("Extra github_token = %q", a.token.ExtraGet(githubTokenExtra))
	}
}
