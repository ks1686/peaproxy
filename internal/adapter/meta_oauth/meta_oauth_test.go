package meta_oauth

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
)

func TestMetaDeviceMintsAPIKeyAndChats(t *testing.T) {
	var deviceForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(r.URL.Path, "authorization"):
			deviceForm, _ = url.ParseQuery(string(raw))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code": "dc-m", "user_code": "MUSE-1",
				"verification_uri": "https://auth.meta.com/device",
				"expires_in":       600, "interval": 0,
			})
		case strings.Contains(r.URL.Path, "token"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "dca:tok", "token_type": "Bearer", "expires_in": 3600,
			})
		case strings.Contains(r.URL.Path, "muse-code/key"):
			if r.Header.Get("Authorization") != "Bearer dca:tok" {
				t.Errorf("mint auth %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"api_key": "muse-key", "user_email": "m@x.y", "base_url": "",
			})
		case r.URL.Path == "/v1/models":
			if r.Header.Get("Authorization") != "Bearer muse-key" {
				t.Errorf("models auth %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "muse"}}})
		case r.URL.Path == "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "hi muse"}}},
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
	if sess.UserCode != "MUSE-1" || deviceForm.Get("client_id") != ClientID {
		t.Fatalf("%#v %v", sess, deviceForm)
	}
	if err := a.AuthComplete(context.Background(), sess, ""); err != nil {
		t.Fatal(err)
	}
	if a.token.AccessToken != "muse-key" || a.token.Email != "m@x.y" {
		t.Fatalf("%#v", a.token)
	}
	if a.token.ExtraGet("dca_token") != "dca:tok" {
		t.Fatalf("dca %#v", a.token.Extra)
	}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || !models[0].SubscriptionOAuth {
		t.Fatalf("%v %#v", err, models)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "muse",
		Raw:   []byte(`{"model":"muse","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil || resp.Content != "hi muse" {
		t.Fatalf("%v %#v", err, resp)
	}
}

func testAdapter(t *testing.T, srv *httptest.Server) *Adapter {
	t.Helper()
	adp, err := New(adapter.Options{ID: "meta-oauth"})
	if err != nil {
		t.Fatal(err)
	}
	a := adp.(*Adapter)
	a.deviceURL = srv.URL + "/oidc/device/authorization/"
	a.tokenURL = srv.URL + "/oidc/device/token/"
	a.mintURL = srv.URL + "/muse-code/key"
	a.apiBase = srv.URL + "/v1"
	a.pollInterval = time.Millisecond
	a.httpClient = &http.Client{Timeout: 5 * time.Second}
	return a
}
