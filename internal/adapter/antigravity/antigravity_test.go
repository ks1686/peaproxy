package antigravity

import (
	"bytes"
	"context"
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

func TestPublicInstalledAppClientAssembled(t *testing.T) {
	if !strings.HasPrefix(ClientID, "1071006060591-") {
		t.Fatalf("client id prefix %s", ClientID)
	}
	if !strings.HasSuffix(ClientID, "."+"apps.googleusercontent.com") {
		t.Fatalf("client id suffix %s", ClientID)
	}
	if !strings.HasPrefix(ClientSecret, "GOC"+"SPX-") {
		t.Fatal("secret prefix")
	}
	if len(ClientSecret) != 35 {
		t.Fatalf("secret len %d", len(ClientSecret))
	}
}

func TestAuthorizeURLUsesGoogleAntigravityClient(t *testing.T) {
	u, err := url.Parse(AuthorizeURL("st-g", RedirectURI))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "accounts.google.com" || !strings.Contains(u.Path, "/o/oauth2/") {
		t.Fatalf("%s %s", u.Host, u.Path)
	}
	q := u.Query()
	if q.Get("client_id") != ClientID {
		t.Fatalf("client_id %s", q.Get("client_id"))
	}
	if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
		t.Fatalf("%v", q)
	}
	if q.Get("redirect_uri") != RedirectURI {
		t.Fatalf("redirect %s", q.Get("redirect_uri"))
	}
	if !strings.Contains(q.Get("scope"), "cloud-platform") {
		t.Fatalf("scope %s", q.Get("scope"))
	}
}

func TestExchangeFormAndProjectDiscovery(t *testing.T) {
	var sawForm url.Values
	var loadBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/token":
			sawForm, _ = url.ParseQuery(string(raw))
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("content-type %s", r.Header.Get("Content-Type"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "ag-at", "refresh_token": "ag-rt", "expires_in": 3600,
			})
		case strings.Contains(r.URL.Path, "userinfo"):
			if r.Header.Get("Authorization") != "Bearer ag-at" {
				t.Errorf("userinfo auth %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"email": "g@x.y"})
		case strings.Contains(r.URL.Path, "loadCodeAssist"):
			loadBody = raw
			if r.Header.Get("Authorization") != "Bearer ag-at" {
				t.Errorf("load auth %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"cloudaicompanionProject": "proj-99"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	a := testAdapter(t, srv)
	a.pending = &pendingAuth{state: "st"}
	if err := a.AuthComplete(context.Background(), adapter.AuthSession{State: "st"}, "code-g"); err != nil {
		t.Fatal(err)
	}
	if sawForm.Get("grant_type") != "authorization_code" || sawForm.Get("code") != "code-g" {
		t.Fatalf("%v", sawForm)
	}
	if sawForm.Get("client_id") != ClientID || sawForm.Get("client_secret") == "" {
		t.Fatalf("client %v", sawForm)
	}
	if a.token.AccessToken != "ag-at" || a.token.Email != "g@x.y" {
		t.Fatalf("%#v", a.token)
	}
	if a.token.AccountID != "proj-99" || a.token.ExtraGet("project_id") != "proj-99" {
		t.Fatalf("project %#v", a.token)
	}
	if !bytes.Contains(loadBody, []byte(`"ideType"`)) {
		t.Fatalf("load body %s", loadBody)
	}
}

func TestListModelsAndChatUseCloudCode(t *testing.T) {
	var modelPath, chatPath, chatAuth, chatUA string
	var chatBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(r.URL.Path, "fetchAvailableModels"):
			modelPath = r.URL.Path
			if r.Header.Get("Authorization") != "Bearer live-at" {
				t.Errorf("models auth %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": map[string]string{"displayName": "Flash"},
				},
			})
		case strings.Contains(r.URL.Path, "generateContent"):
			chatPath = r.URL.Path
			chatAuth = r.Header.Get("Authorization")
			chatUA = r.Header.Get("User-Agent")
			chatBody = raw
			_ = json.NewEncoder(w).Encode(map[string]any{
				"response": map[string]any{
					"candidates": []map[string]any{{
						"content": map[string]any{
							"parts": []map[string]string{{"text": "hello gemini"}},
						},
					}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv)
	a.token = oauth.Token{
		AccessToken: "live-at",
		ExpiresAt:   time.Now().Add(time.Hour),
		AccountID:   "proj-1",
		Extra:       map[string]string{"project_id": "proj-1"},
	}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "gemini-2.5-flash" || !models[0].SubscriptionOAuth {
		t.Fatalf("%v %#v", err, models)
	}
	if !strings.Contains(modelPath, "fetchAvailableModels") {
		t.Fatalf("path %s", modelPath)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model:    "gemini-2.5-flash",
		Messages: []adapter.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil || resp.Content != "hello gemini" {
		t.Fatalf("%v %#v", err, resp)
	}
	if chatAuth != "Bearer live-at" {
		t.Fatalf("auth %q", chatAuth)
	}
	if !strings.Contains(chatPath, "generateContent") {
		t.Fatalf("chat path %s", chatPath)
	}
	if !bytes.Contains(chatBody, []byte(`"project"`)) || !bytes.Contains(chatBody, []byte("hi")) {
		t.Fatalf("chat body %s", chatBody)
	}
	if chatUA != UserAgent || strings.Contains(chatUA, "linux/amd64") || strings.Contains(chatUA, "2.9.1") {
		t.Fatalf("user agent %s", chatUA)
	}
	for _, needle := range []string{`"userAgent":"antigravity"`, `"requestType":"agent"`, `"requestId":"agent-`} {
		if !bytes.Contains(chatBody, []byte(needle)) {
			t.Fatalf("missing %s in %s", needle, chatBody)
		}
	}
}

func TestChatKeepsImageAndTool(t *testing.T) {
	var chatBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "generateContent") {
			chatBody, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"response": map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]string{"text": "seen"}}}}}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv)
	a.token = oauth.Token{AccessToken: "live-at", ExpiresAt: time.Now().Add(time.Hour), Extra: map[string]string{"project_id": "proj-1"}}
	raw := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/pea.png"}}]}],"tools":[{"type":"function","function":{"name":"lookup","description":"find","parameters":{"type":"object"}}}]}`)
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "gemini-2.5-flash", Raw: raw})
	if err != nil || resp.Content != "seen" {
		t.Fatalf("%v %#v", err, resp)
	}
	for _, needle := range []string{`"fileUri":"https://example.com/pea.png"`, `"name":"lookup"`, `"text":"look"`} {
		if !bytes.Contains(chatBody, []byte(needle)) {
			t.Fatalf("missing %s in %s", needle, chatBody)
		}
	}
}

func TestValidateRequiresToken(t *testing.T) {
	a := testAdapter(t, httptest.NewServer(http.NotFoundHandler()))
	if err := a.Validate(context.Background()); err == nil {
		t.Fatal("expected auth required")
	}
}

func testAdapter(t *testing.T, srv *httptest.Server) *Adapter {
	t.Helper()
	adp, err := New(adapter.Options{ID: "antigravity", SkipLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	a := adp.(*Adapter)
	a.tokenURL = srv.URL + "/token"
	a.userInfoURL = srv.URL + "/oauth2/v2/userinfo"
	a.apiBase = srv.URL
	a.dailyAPI = srv.URL
	a.httpClient = &http.Client{Timeout: 5 * time.Second}
	return a
}
