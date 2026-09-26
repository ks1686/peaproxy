package anthropic_oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/oauth"
)

func TestAuthURLIncludesPKCEAndClaudeClient(t *testing.T) {
	pkce := oauth.PKCE{Verifier: "v", Challenge: "chal"}
	u, err := url.Parse(AuthorizeURL(pkce, "st-1"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "claude.ai" || u.Path != "/oauth/authorize" {
		t.Fatalf("host/path %s %s", u.Host, u.Path)
	}
	q := u.Query()
	if q.Get("client_id") != ClientID {
		t.Fatalf("client_id %s", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != RedirectURI {
		t.Fatalf("redirect %s", q.Get("redirect_uri"))
	}
	if q.Get("code_challenge") != "chal" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("pkce %v", q)
	}
	if q.Get("state") != "st-1" {
		t.Fatalf("state %s", q.Get("state"))
	}
	if !strings.Contains(q.Get("scope"), "user:inference") {
		t.Fatalf("scope %s", q.Get("scope"))
	}
}

func TestExchangeAndRefreshUseJSONBodies(t *testing.T) {
	var seen []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen = append(seen, r.URL.Path)
		bodies = append(bodies, string(raw))
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("User-Agent") != TokenUserAgent {
			t.Errorf("user-agent %s", r.Header.Get("User-Agent"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-new",
			"refresh_token": "rt-new",
			"expires_in":    3600,
			"account":       map[string]string{"email_address": "a@b.c", "uuid": "acc"},
			"organization":  map[string]string{"uuid": "org", "name": "Org"},
		})
	}))
	t.Cleanup(srv.Close)

	a := testAdapter(t, srv.URL)
	a.pending = &pendingAuth{pkce: oauth.PKCE{Verifier: "ver"}, state: "st"}
	if err := a.AuthComplete(context.Background(), adapter.AuthSession{State: "st"}, "code-1"); err != nil {
		t.Fatal(err)
	}
	if a.token.AccessToken != "at-new" || a.token.RefreshToken != "rt-new" {
		t.Fatalf("%#v", a.token)
	}
	if a.token.Email != "a@b.c" {
		t.Fatalf("email %s", a.token.Email)
	}
	if !bytes.Contains([]byte(bodies[0]), []byte(`"grant_type":"authorization_code"`)) {
		t.Fatalf("exchange body %s", bodies[0])
	}
	if !bytes.Contains([]byte(bodies[0]), []byte(`"code_verifier":"ver"`)) {
		t.Fatalf("missing verifier %s", bodies[0])
	}
	// Field order: grant_type, code, redirect_uri, client_id, code_verifier, state
	if i := strings.Index(bodies[0], `"grant_type"`); i < 0 || strings.Index(bodies[0], `"code"`) < i {
		t.Fatalf("field order %s", bodies[0])
	}

	a.token.ExpiresAt = time.Now().Add(-time.Minute)
	if err := a.ensureToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(bodies) < 2 || !strings.Contains(bodies[1], `"grant_type":"refresh_token"`) {
		t.Fatalf("refresh bodies %#v", bodies)
	}
}

func TestChatUsesBearerAndOAuthBeta(t *testing.T) {
	var gotAuth, gotKey, gotBeta, gotVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.Header.Get("x-api-key")
		gotBeta = r.Header.Get("anthropic-beta")
		gotVer = r.Header.Get("anthropic-version")
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/models"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "claude-sonnet-4-20250514", "display_name": "Sonnet"}},
			})
		case strings.HasSuffix(r.URL.Path, "/v1/messages"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_1", "model": "claude-sonnet-4-20250514",
				"content":     []map[string]string{{"type": "text", "text": "hi oauth"}},
				"stop_reason": "end_turn",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "oauth-at", ExpiresAt: time.Now().Add(time.Hour)}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || !models[0].SubscriptionOAuth {
		t.Fatalf("%v %#v", err, models)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "claude-sonnet-4-20250514",
		Raw:   []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil || resp.Content != "hi oauth" {
		t.Fatalf("%v %#v", err, resp)
	}
	if gotAuth != "Bearer oauth-at" {
		t.Fatalf("auth %q", gotAuth)
	}
	if gotKey != "" {
		t.Fatalf("x-api-key must be empty, got %q", gotKey)
	}
	if !strings.Contains(gotBeta, "oauth-2025-04-20") || !strings.Contains(gotBeta, "claude-code-20250219") {
		t.Fatalf("beta %q", gotBeta)
	}
	if gotVer == "" {
		t.Fatal("missing anthropic-version")
	}
}

func TestValidateRequiresToken(t *testing.T) {
	a := testAdapter(t, "http://127.0.0.1:9")
	if err := a.Validate(context.Background()); err == nil {
		t.Fatal("expected auth required")
	}
}

func TestTokenEndpoint403ExplainsCloudflare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "blocked")
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.pending = &pendingAuth{pkce: oauth.PKCE{Verifier: "ver"}, state: "st"}
	err := a.AuthComplete(context.Background(), adapter.AuthSession{State: "st"}, "code-1")
	if err == nil {
		t.Fatal("expected 403")
	}
	msg := err.Error()
	if !strings.Contains(msg, "403") || !strings.Contains(msg, "Cloudflare") {
		t.Fatalf("want Cloudflare 403 hint, got %v", err)
	}
	if !strings.Contains(msg, "console.anthropic.com") || !strings.Contains(msg, "docs/OAUTH.md") {
		t.Fatalf("want key workaround and docs pointer, got %v", err)
	}
	var he adapter.HTTPError
	if !errors.As(err, &he) || he.Status != 403 {
		t.Fatalf("want HTTPError 403, got %T %v", err, err)
	}
}

func testAdapter(t *testing.T, base string) *Adapter {
	t.Helper()
	adp, err := New(adapter.Options{ID: "anthropic-oauth", BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	a := adp.(*Adapter)
	a.tokenURL = base + "/v1/oauth/token"
	a.httpClient = &http.Client{Timeout: 5 * time.Second}
	return a
}
