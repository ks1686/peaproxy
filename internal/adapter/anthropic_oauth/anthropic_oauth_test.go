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

func TestMessagesUsesClaudeCodeOAuthFingerprint(t *testing.T) {
	var got *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.NotFound(w, r)
			return
		}
		got = r.Clone(r.Context())
		body, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "model": "claude-sonnet-5",
			"content":     []map[string]string{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{
		AccessToken: "oauth-at",
		AccountID:   "11111111-1111-4111-8111-111111111111",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "claude-sonnet-5",
		Raw:   []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("no /v1/messages request")
	}
	if got.URL.Query().Get("beta") != "true" {
		t.Fatalf("want ?beta=true like Claude Code / CLIProxyAPI, got %s", got.URL.String())
	}
	const wantUA = "claude-cli/2.1.280 (external, cli)"
	if ua := got.Header.Get("User-Agent"); ua != wantUA {
		t.Fatalf("User-Agent %q, want Claude Code CLI %q (Go-http-client/1.1 is a bot signature)", ua, wantUA)
	}
	if got.Header.Get("Authorization") != "Bearer oauth-at" {
		t.Fatalf("Authorization %q", got.Header.Get("Authorization"))
	}
	if got.Header.Get("x-api-key") != "" {
		t.Fatalf("x-api-key must be empty for OAuth, got %q", got.Header.Get("x-api-key"))
	}
	if got.Header.Get("anthropic-version") != APIVersion {
		t.Fatalf("anthropic-version %q", got.Header.Get("anthropic-version"))
	}
	if got.Header.Get("x-app") != "cli" {
		t.Fatalf("x-app %q", got.Header.Get("x-app"))
	}
	if got.Header.Get("anthropic-dangerous-direct-browser-access") != "true" {
		t.Fatalf("missing anthropic-dangerous-direct-browser-access, got %q", got.Header.Get("anthropic-dangerous-direct-browser-access"))
	}
	if got.Header.Get("Accept") != "application/json" {
		t.Fatalf("Accept %q, Claude Code uses application/json even on api.anthropic.com streams", got.Header.Get("Accept"))
	}
	if got.Header.Get("X-Stainless-Lang") != "js" || got.Header.Get("X-Stainless-Runtime") != "node" {
		t.Fatalf("stainless lang/runtime %q %q", got.Header.Get("X-Stainless-Lang"), got.Header.Get("X-Stainless-Runtime"))
	}
	if got.Header.Get("X-Stainless-Package-Version") != "0.112.1" {
		t.Fatalf("package version %q", got.Header.Get("X-Stainless-Package-Version"))
	}
	if got.Header.Get("X-Stainless-Runtime-Version") != "v26.3.0" {
		t.Fatalf("runtime version %q", got.Header.Get("X-Stainless-Runtime-Version"))
	}
	if got.Header.Get("X-Stainless-Retry-Count") != "0" || got.Header.Get("X-Stainless-Timeout") != "600" {
		t.Fatalf("stainless retry/timeout %q %q", got.Header.Get("X-Stainless-Retry-Count"), got.Header.Get("X-Stainless-Timeout"))
	}
	if got.Header.Get("X-Stainless-Os") == "" || got.Header.Get("X-Stainless-Arch") == "" {
		t.Fatal("missing stainless OS/Arch")
	}
	if !uuidLike(got.Header.Get("x-client-request-id")) {
		t.Fatalf("x-client-request-id %q", got.Header.Get("x-client-request-id"))
	}
	betas := got.Header.Get("anthropic-beta")
	for _, want := range []string{
		"claude-code-20250219",
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"redact-thinking-2026-02-12",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"mid-conversation-system-2026-04-07",
		"extended-cache-ttl-2025-04-11",
	} {
		if !strings.Contains(betas, want) {
			t.Fatalf("anthropic-beta %q missing %s", betas, want)
		}
	}
	if strings.Contains(betas, "mid-conversation-tool-changes-2026-07-01") {
		t.Fatalf("sonnet-5 must omit mid-conversation-tool-changes, got %q", betas)
	}

	var payload struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Metadata  struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != "claude-sonnet-5" {
		t.Fatalf("model %s", payload.Model)
	}
	if payload.MaxTokens <= 0 {
		t.Fatalf("max_tokens %d", payload.MaxTokens)
	}
	var ident struct {
		DeviceID    string `json:"device_id"`
		AccountUUID string `json:"account_uuid"`
		SessionID   string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(payload.Metadata.UserID), &ident); err != nil {
		t.Fatalf("metadata.user_id must be Claude Code JSON string, got %q: %v", payload.Metadata.UserID, err)
	}
	if len(ident.DeviceID) != 64 {
		t.Fatalf("device_id %q", ident.DeviceID)
	}
	if ident.AccountUUID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("account_uuid %q", ident.AccountUUID)
	}
	if !uuidLike(ident.SessionID) {
		t.Fatalf("session_id %q", ident.SessionID)
	}
	if got.Header.Get("X-Claude-Code-Session-Id") != ident.SessionID {
		t.Fatalf("session header %q != body %q", got.Header.Get("X-Claude-Code-Session-Id"), ident.SessionID)
	}
}

func TestMessagesCanonicalizesClaudeSonnet5Aliases(t *testing.T) {
	var seenModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &payload)
		seenModel = payload.Model
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "model": payload.Model,
			"content":     []map[string]string{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "oauth-at", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "anthropic/claude-sonnet-5",
		Raw:   []byte(`{"model":"anthropic/claude-sonnet-5","max_tokens":32,"system":"x","messages":[{"role":"user","content":"hi"}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if seenModel != "claude-sonnet-5" {
		t.Fatalf("passthrough model %q, want claude-sonnet-5 (strip anthropic/ prefix)", seenModel)
	}
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "claude-sonnet-4.5",
		Raw:   []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if seenModel != "claude-sonnet-4-5" {
		t.Fatalf("dotted alias %q, want claude-sonnet-4-5", seenModel)
	}
}

func TestMessagesStreamKeepsJSONAccept(t *testing.T) {
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.NotFound(w, r)
			return
		}
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-5\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "oauth-at", ExpiresAt: time.Now().Add(time.Hour)}
	var buf bytes.Buffer
	if err := a.ChatStream(context.Background(), adapter.ChatRequest{
		Model:  "claude-sonnet-5",
		Stream: true,
		Raw:    []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`),
	}, &buf); err != nil {
		t.Fatal(err)
	}
	if gotAccept != "application/json" {
		t.Fatalf("stream Accept %q, want application/json (Claude Code on api.anthropic.com)", gotAccept)
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

func uuidLike(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}
