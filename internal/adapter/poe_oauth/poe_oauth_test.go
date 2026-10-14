package poe_oauth

import (
	"context"
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

func TestAuthorizeURLUsesPublicClient(t *testing.T) {
	raw := AuthorizeURL("http://127.0.0.1:9/callback", "chal", "st")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "poe.com" || u.Path != "/oauth/authorize" {
		t.Fatalf("url %s", raw)
	}
	if q.Get("client_id") != ClientID || q.Get("scope") != Scope || q.Get("redirect_uri") != "http://127.0.0.1:9/callback" {
		t.Fatalf("query %s", q.Encode())
	}
	if q.Get("code_challenge") != "chal" || q.Get("code_challenge_method") != "S256" || q.Get("state") != "st" {
		t.Fatalf("pkce query %s", q.Encode())
	}
	if q.Get("response_type") != "code" {
		t.Fatalf("response_type %s", q.Get("response_type"))
	}
}

func TestLoginThenChatUsesAPIKey(t *testing.T) {
	var sawAuth bool
	var gotRedirect, gotVerifier, gotClient string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			body, _ := io.ReadAll(r.Body)
			form, err := url.ParseQuery(string(body))
			if err != nil {
				t.Errorf("form: %v", err)
			}
			gotRedirect = form.Get("redirect_uri")
			gotVerifier = form.Get("code_verifier")
			gotClient = form.Get("client_id")
			if form.Get("grant_type") != "authorization_code" || form.Get("code") != "auth-code" {
				t.Errorf("grant %s code %s", form.Get("grant_type"), form.Get("code"))
			}
			_, _ = w.Write([]byte(`{"api_key":"poe-key","api_key_expires_in":3600}`))
		case r.URL.Path == "/v1/models":
			if r.Header.Get("Authorization") != "Bearer poe-key" {
				t.Errorf("models auth %s", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"poe/test"}]}`))
		case r.URL.Path == "/v1/chat/completions":
			sawAuth = r.Header.Get("Authorization") == "Bearer poe-key"
			_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	raw, err := New(adapter.Options{ID: "poe"})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Adapter)
	a.tokenURL = srv.URL + "/token"
	a.apiBase = srv.URL + "/v1"
	a.httpClient = srv.Client()
	t.Cleanup(func() {
		a.mu.Lock()
		p := a.pending
		a.mu.Unlock()
		if p != nil && p.lb != nil {
			_ = p.lb.Close()
		}
	})

	sess, err := a.AuthStart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sess.LoginURL, ClientID) || sess.State == "" {
		t.Fatalf("session %#v", sess)
	}
	a.mu.Lock()
	cb := a.pending.redirect + "?code=auth-code&state=" + url.QueryEscape(sess.State)
	verifier := a.pending.pkce.Verifier
	redirect := a.pending.redirect
	a.mu.Unlock()
	resp, err := http.Get(cb)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback %d", resp.StatusCode)
	}
	if err := a.AuthComplete(context.Background(), sess, ""); err != nil {
		t.Fatal(err)
	}
	if gotClient != ClientID || gotRedirect != redirect || gotVerifier != verifier || gotVerifier == "" {
		t.Fatalf("client %s redirect %s verifier set %v", gotClient, gotRedirect, gotVerifier != "")
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "poe/test" || !models[0].SubscriptionOAuth || models[0].Provider != Name {
		t.Fatalf("models %#v", models)
	}
	chat, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "poe/test", Messages: []adapter.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if chat.Content != "ok" || !sawAuth {
		t.Fatalf("content %q auth %v", chat.Content, sawAuth)
	}
	if a.token.ExpiresAt.Before(time.Now().Add(30 * time.Minute)) {
		t.Fatalf("expiry %s", a.token.ExpiresAt)
	}
}

func TestIssuerMismatchDoesNotExchange(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	raw, err := New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Adapter)
	a.tokenURL = srv.URL + "/token"
	a.httpClient = srv.Client()
	t.Cleanup(func() {
		a.mu.Lock()
		p := a.pending
		a.mu.Unlock()
		if p != nil && p.lb != nil {
			_ = p.lb.Close()
		}
	})
	sess, err := a.AuthStart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	cb := a.pending.redirect + "?code=auth-code&state=" + url.QueryEscape(sess.State) + "&iss=" + url.QueryEscape("https://evil.example")
	a.mu.Unlock()
	resp, err := http.Get(cb)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	err = a.AuthComplete(context.Background(), sess, "")
	if err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("token endpoint hits %d", hits)
	}
}

func TestPrintURLRefused(t *testing.T) {
	raw, err := New(adapter.Options{SkipLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.(adapter.Authenticator).AuthStart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--print-url") {
		t.Fatal(err)
	}
}

func TestExpiredKeyRefusesChat(t *testing.T) {
	a := &Adapter{id: "poe", token: oauth.Token{AccessToken: "poe-key", ExpiresAt: time.Now().Add(-time.Minute)}}
	_, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "sign in again") {
		t.Fatal(err)
	}
}

func TestTokenDetailOmitsSecrets(t *testing.T) {
	if got := safeDetail([]byte(`{"error_description":"bad code auth-code"}`), "auth-code", "ver"); got != "" {
		t.Fatalf("detail leaked: %q", got)
	}
	if got := safeDetail([]byte(`{"error":"invalid_grant"}`), "auth-code"); !strings.Contains(got, "invalid_grant") {
		t.Fatalf("detail %q", got)
	}
}
