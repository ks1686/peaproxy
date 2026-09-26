package copilot_oauth

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

func TestAuthStartPostsGitHubDeviceCode(t *testing.T) {
	var form url.Values
	polls := 0
	var chatAuth, modelsAuth, chatIntegration string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/login/device/code"):
			form, _ = url.ParseQuery(string(raw))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":               "dc-copilot",
				"user_code":                 "ABCD-1234",
				"verification_uri":          "https://github.com/login/device",
				"verification_uri_complete": "https://github.com/login/device?user_code=ABCD-1234",
				"expires_in":                600,
				"interval":                  0,
			})
		case strings.HasSuffix(r.URL.Path, "/login/oauth/access_token"):
			polls++
			q, _ := url.ParseQuery(string(raw))
			if q.Get("grant_type") != oauth.DeviceGrantType {
				t.Errorf("grant %s", q.Get("grant_type"))
			}
			if q.Get("client_id") != ClientID {
				t.Errorf("client_id %s", q.Get("client_id"))
			}
			if polls == 1 {
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "ghu_github", "token_type": "bearer"})
		case strings.HasSuffix(r.URL.Path, "/copilot_internal/v2/token"):
			if r.Method != http.MethodGet {
				t.Errorf("copilot token method %s", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer ghu_github" {
				t.Errorf("copilot token auth %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "tid=session;exp=9999999999",
				"expires_at": time.Now().Add(time.Hour).Unix(),
			})
		case r.URL.Path == "/user":
			if r.Header.Get("Authorization") != "Bearer ghu_github" {
				t.Errorf("user auth %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "octocat", "email": "octocat@github.com"})
		case r.URL.Path == "/models":
			modelsAuth = r.Header.Get("Authorization")
			if r.Header.Get("Copilot-Integration-Id") == "" {
				t.Error("models missing Copilot-Integration-Id")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"id":                   "gpt-4.1",
						"model_picker_enabled": true,
						"policy":               map[string]string{"state": "enabled"},
						"capabilities":         map[string]any{"supports": map[string]bool{"vision": true, "streaming": true}},
					},
					{
						"id":                   "hidden-model",
						"model_picker_enabled": false,
					},
					{
						"id":     "disabled-model",
						"policy": map[string]string{"state": "disabled"},
					},
				},
			})
		case r.URL.Path == "/chat/completions":
			chatAuth = r.Header.Get("Authorization")
			chatIntegration = r.Header.Get("Copilot-Integration-Id")
			if strings.Contains(string(raw), `"stream":true`) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi copilot\"}}]}\n\n")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "hi copilot"}}},
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	a := testAdapter(t, srv)
	sess, err := a.AuthStart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sess.UserCode != "ABCD-1234" || !strings.Contains(sess.LoginURL, "github.com/login/device") {
		t.Fatalf("%#v", sess)
	}
	if form.Get("client_id") != ClientID || form.Get("scope") != Scope {
		t.Fatalf("%v", form)
	}
	if err := a.AuthComplete(context.Background(), sess, ""); err != nil {
		t.Fatal(err)
	}
	if a.token.AccessToken != "tid=session;exp=9999999999" || a.token.RefreshToken != "ghu_github" {
		t.Fatalf("%#v", a.token)
	}
	if a.token.Email != "octocat@github.com" && a.token.Email != "octocat" {
		t.Fatalf("email %#v", a.token.Email)
	}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "gpt-4.1" || !models[0].SubscriptionOAuth {
		t.Fatalf("%v %#v", err, models)
	}
	if !contains(models[0].Modalities, "image_in") {
		t.Fatalf("vision: %#v", models[0].Modalities)
	}
	if modelsAuth != "Bearer tid=session;exp=9999999999" {
		t.Fatalf("models auth %s", modelsAuth)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "gpt-4.1",
		Raw:   []byte(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil || resp.Content != "hi copilot" {
		t.Fatalf("%v %#v", err, resp)
	}
	if chatAuth != "Bearer tid=session;exp=9999999999" || chatIntegration != IntegrationID {
		t.Fatalf("chat headers auth=%s integration=%s", chatAuth, chatIntegration)
	}
	var buf bytes.Buffer
	if err := a.ChatStream(context.Background(), adapter.ChatRequest{
		Model: "gpt-4.1",
		Raw:   []byte(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}]}`),
	}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "hi copilot") {
		t.Fatalf("stream %s", buf.String())
	}
}

func TestCopilotOAuthRefusesImageOutAndEmbeddings(t *testing.T) {
	a := testAdapter(t, httptest.NewServer(http.NotFoundHandler()))
	a.token = oauth.Token{AccessToken: "tok", RefreshToken: "ghu", ExpiresAt: time.Now().Add(time.Hour)}
	if a.Capabilities().ImageOut || a.Capabilities().Embeddings {
		t.Fatalf("oauth must not advertise image-out/embeddings: %#v", a.Capabilities())
	}
	if _, ok := adapter.Adapter(a).(adapter.ImageGenerator); ok {
		t.Fatal("must not implement ImageGenerator")
	}
	if _, ok := adapter.Adapter(a).(adapter.Embedder); ok {
		t.Fatal("must not implement Embedder")
	}
}

func TestValidateRequiresToken(t *testing.T) {
	a := testAdapter(t, httptest.NewServer(http.NotFoundHandler()))
	if err := a.Validate(context.Background()); err == nil {
		t.Fatal("expected auth required")
	}
}

func TestRefreshExchangesGitHubToken(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/copilot_internal/v2/token") {
			http.NotFound(w, r)
			return
		}
		hits++
		if r.Header.Get("Authorization") != "Bearer ghu_old" {
			t.Errorf("refresh auth %s", r.Header.Get("Authorization"))
		}
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
	if a.token.AccessToken != "tid=fresh" || hits != 1 {
		t.Fatalf("token %#v hits %d", a.token, hits)
	}
}

func TestAPIBaseFromSessionToken(t *testing.T) {
	got := apiBaseFromSession("tid=x;proxy-ep=proxy.contoso.githubcopilot.com;exp=1", DefaultAPIBase)
	if got != "https://api.contoso.githubcopilot.com" {
		t.Fatalf("%s", got)
	}
	if apiBaseFromSession("tid=x", DefaultAPIBase) != DefaultAPIBase {
		t.Fatal("fallback")
	}
	if strings.Contains(DefaultAPIBase, "models.inference.ai.azure.com") || strings.Contains(DefaultAPIBase, "/v1") {
		t.Fatalf("must be Copilot chat host without GitHub Models and without /v1: %s", DefaultAPIBase)
	}
}

func TestClientIDIsPublicCopilotGitHubApp(t *testing.T) {
	if ClientID != "Iv1.b507a08c87ecfe98" {
		t.Fatalf("expected public VS Code Copilot GitHub App client id, got %s", ClientID)
	}
	if Scope != "read:user" {
		t.Fatalf("scope %s", Scope)
	}
}

func testAdapter(t *testing.T, srv *httptest.Server) *Adapter {
	t.Helper()
	adp, err := New(adapter.Options{ID: "copilot-oauth"})
	if err != nil {
		t.Fatal(err)
	}
	a := adp.(*Adapter)
	a.deviceURL = srv.URL + "/login/device/code"
	a.tokenURL = srv.URL + "/login/oauth/access_token"
	a.copilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	a.userURL = srv.URL + "/user"
	a.apiBase = srv.URL
	a.pollInterval = time.Millisecond
	a.httpClient = &http.Client{Timeout: 5 * time.Second}
	return a
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
