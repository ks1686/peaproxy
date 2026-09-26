package xai_oauth

import (
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

func TestAuthStartPostsDeviceCode(t *testing.T) {
	var form url.Values
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/device"):
			form, _ = url.ParseQuery(string(raw))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code": "dc-x", "user_code": "GROK-1",
				"verification_uri":          "https://auth.x.ai/device",
				"verification_uri_complete": "https://auth.x.ai/device?user_code=GROK-1",
				"expires_in":                600, "interval": 0,
			})
		case strings.HasSuffix(r.URL.Path, "/token"):
			polls++
			q, _ := url.ParseQuery(string(raw))
			if q.Get("grant_type") != oauth.DeviceGrantType {
				t.Errorf("grant %s", q.Get("grant_type"))
			}
			if polls == 1 {
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "x-at", "refresh_token": "x-rt", "expires_in": 3600,
			})
		case r.URL.Path == "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "grok-4"}}})
		case r.URL.Path == "/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer x-at" {
				t.Errorf("chat auth %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "hi grok"}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	a := testAdapter(t, srv)
	sess, err := a.AuthStart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sess.UserCode != "GROK-1" || !strings.Contains(sess.LoginURL, "auth.x.ai") {
		t.Fatalf("%#v", sess)
	}
	if form.Get("client_id") != ClientID || !strings.Contains(form.Get("scope"), "grok-cli:access") {
		t.Fatalf("%v", form)
	}
	if err := a.AuthComplete(context.Background(), sess, ""); err != nil {
		t.Fatal(err)
	}
	if a.token.AccessToken != "x-at" {
		t.Fatalf("%#v", a.token)
	}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "grok-4" || !models[0].SubscriptionOAuth {
		t.Fatalf("%v %#v", err, models)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "grok-4",
		Raw:   []byte(`{"model":"grok-4","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil || resp.Content != "hi grok" {
		t.Fatalf("%v %#v", err, resp)
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
	adp, err := New(adapter.Options{ID: "xai-oauth"})
	if err != nil {
		t.Fatal(err)
	}
	a := adp.(*Adapter)
	a.discoveryURL = ""
	a.deviceURL = srv.URL + "/device"
	a.tokenURL = srv.URL + "/token"
	a.apiBase = srv.URL + "/v1"
	a.pollInterval = time.Millisecond
	a.httpClient = &http.Client{Timeout: 5 * time.Second}
	return a
}

func TestClientIDIsPublicGrokCLI(t *testing.T) {
	if ClientID == "" || !strings.Contains(Scope, "offline_access") {
		t.Fatalf("client/scope %s %s", ClientID, Scope)
	}
}
