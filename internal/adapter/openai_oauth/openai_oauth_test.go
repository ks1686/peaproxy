package openai_oauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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

func TestAuthURLIncludesCodexClientAndPKCE(t *testing.T) {
	pkce := oauth.PKCE{Verifier: "v", Challenge: "chal"}
	u, err := url.Parse(AuthorizeURL(pkce, "st-9"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "auth.openai.com" || u.Path != "/oauth/authorize" {
		t.Fatalf("%s %s", u.Host, u.Path)
	}
	q := u.Query()
	if q.Get("client_id") != ClientID {
		t.Fatalf("client_id %s", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != RedirectURI {
		t.Fatalf("redirect %s", q.Get("redirect_uri"))
	}
	if q.Get("code_challenge") != "chal" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("%v", q)
	}
	if q.Get("codex_cli_simplified_flow") != "true" {
		t.Fatalf("missing simplified flow: %v", q)
	}
	if !strings.Contains(q.Get("scope"), "offline_access") {
		t.Fatalf("scope %s", q.Get("scope"))
	}
}

func TestExchangeIsFormEncodedAndParsesJWTAccount(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"email": "c@d.e",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_99",
			"chatgpt_plan_type":  "plus",
		},
	})
	idTok := "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(raw))
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("content-type %s", r.Header.Get("Content-Type"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "codex-at",
			"refresh_token": "codex-rt",
			"id_token":      idTok,
			"expires_in":    3600,
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.pending = &pendingAuth{pkce: oauth.PKCE{Verifier: "ver"}, state: "st"}
	if err := a.AuthComplete(context.Background(), adapter.AuthSession{State: "st"}, "code-x"); err != nil {
		t.Fatal(err)
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code_verifier") != "ver" {
		t.Fatalf("%v", form)
	}
	if a.token.AccessToken != "codex-at" || a.token.AccountID != "acct_99" || a.token.Email != "c@d.e" {
		t.Fatalf("%#v", a.token)
	}
}

func TestChatPostsResponsesWithBearerAndAccountHeader(t *testing.T) {
	var gotPath, gotAuth, gotAcct, gotOrig string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAcct = r.Header.Get("Chatgpt-Account-Id")
		gotOrig = r.Header.Get("Originator")
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "gpt-5"}},
			})
		case strings.HasSuffix(r.URL.Path, "/responses"):
			body, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    "resp_1",
				"model": "gpt-5",
				"output": []map[string]any{{
					"type": "message",
					"content": []map[string]string{{
						"type": "output_text", "text": "hello codex",
					}},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || !models[0].SubscriptionOAuth {
		t.Fatalf("%v %#v", err, models)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "gpt-5",
		Raw:   []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil || resp.Content != "hello codex" {
		t.Fatalf("%v %#v", err, resp)
	}
	if !strings.HasSuffix(gotPath, "/responses") {
		t.Fatalf("path %s", gotPath)
	}
	if gotAuth != "Bearer tok" || gotAcct != "acct_99" {
		t.Fatalf("auth=%q acct=%q", gotAuth, gotAcct)
	}
	if gotOrig == "" {
		t.Fatal("missing Originator")
	}
	if bytes.Contains(body, []byte(`"messages"`)) {
		t.Fatalf("should send Responses input, got %s", body)
	}
	if !bytes.Contains(body, []byte(`"input"`)) {
		t.Fatalf("missing input: %s", body)
	}
}

func TestResponsesPassthroughKeepsInput(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          "resp_native",
			"object":      "response",
			"output_text": "native",
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}
	raw := []byte(`{"model":"gpt-5","input":"codex ping"}`)
	out, err := a.Responses(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(gotBody, []byte(`"input"`)) || bytes.Contains(gotBody, []byte(`"messages"`)) {
		t.Fatalf("passthrough body %s", gotBody)
	}
	if !bytes.Contains(out, []byte(`"output_text":"native"`)) {
		t.Fatalf("native response %s", out)
	}
}

func TestChatToResponsesPreservesModelAndUserText(t *testing.T) {
	out, err := chatToResponses([]byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`), "gpt-5", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"model":"gpt-5"`)) || !bytes.Contains(out, []byte("hello")) {
		t.Fatalf("%s", out)
	}
	if bytes.Contains(out, []byte(`"messages"`)) {
		t.Fatalf("leaked chat messages: %s", out)
	}
}

func TestRefreshFormGrant(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(raw))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-at",
			"expires_in":   120,
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{RefreshToken: "old-rt", ExpiresAt: time.Now().Add(-time.Minute)}
	if err := a.ensureToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "old-rt" {
		t.Fatalf("%v", form)
	}
	if a.token.AccessToken != "new-at" || a.token.RefreshToken != "old-rt" {
		t.Fatalf("%#v", a.token)
	}
}

func testAdapter(t *testing.T, base string) *Adapter {
	t.Helper()
	adp, err := New(adapter.Options{ID: "openai-oauth", BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	a := adp.(*Adapter)
	a.tokenURL = base + "/oauth/token"
	a.apiBase = strings.TrimRight(base, "/")
	a.httpClient = &http.Client{Timeout: 5 * time.Second}
	return a
}
