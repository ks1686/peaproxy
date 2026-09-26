package kimi_oauth

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

func TestKimiDeviceAndChat(t *testing.T) {
	var deviceForm, tokenForm url.Values
	var deviceID, platform string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		q, _ := url.ParseQuery(string(raw))
		switch {
		case strings.Contains(r.URL.Path, "device_authorization"):
			deviceForm = q
			deviceID = r.Header.Get("X-Msh-Device-Id")
			platform = r.Header.Get("X-Msh-Platform")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code": "dc-k", "user_code": "KIMI-1",
				"verification_uri_complete": "https://auth.kimi.com/device?user_code=KIMI-1",
				"expires_in":                600, "interval": 0,
			})
		case strings.HasSuffix(r.URL.Path, "/token"):
			tokenForm = q
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "k-at", "refresh_token": "k-rt", "expires_in": 3600,
			})
		case r.URL.Path == "/coding/v1/models":
			if r.Header.Get("Authorization") != "Bearer k-at" {
				t.Errorf("models auth %s", r.Header.Get("Authorization"))
			}
			if r.Header.Get("X-Msh-Device-Id") == "" {
				t.Error("missing device id on models")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "kimi-k2"}}})
		case r.URL.Path == "/coding/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "hi kimi"}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	a := testAdapter(t, srv, false)
	sess, err := a.AuthStart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sess.UserCode != "KIMI-1" {
		t.Fatalf("%#v", sess)
	}
	if deviceForm.Get("client_id") != ClientID {
		t.Fatalf("%v", deviceForm)
	}
	if platform != "PeaProxy" || deviceID == "" {
		t.Fatalf("headers platform=%s device=%s", platform, deviceID)
	}
	if err := a.AuthComplete(context.Background(), sess, ""); err != nil {
		t.Fatal(err)
	}
	if tokenForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
		t.Fatalf("%v", tokenForm)
	}
	if a.token.AccessToken != "k-at" || a.token.ExtraGet("device_id") == "" {
		t.Fatalf("%#v", a.token)
	}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "kimi-k2" || !models[0].SubscriptionOAuth {
		t.Fatalf("%v %#v", err, models)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "kimi-k2",
		Raw:   []byte(`{"model":"kimi-k2","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil || resp.Content != "hi kimi" {
		t.Fatalf("%v %#v", err, resp)
	}
}

func TestKimiAIUsesAIHosts(t *testing.T) {
	if !strings.Contains(AIAPIBase, "kimi.ai") || !strings.Contains(AIOAuthHost, "kimi.ai") {
		t.Fatalf("%s %s", AIAPIBase, AIOAuthHost)
	}
}

func testAdapter(t *testing.T, srv *httptest.Server, ai bool) *Adapter {
	t.Helper()
	opts := adapter.Options{ID: "kimi-oauth"}
	var adp adapter.Adapter
	var err error
	if ai {
		adp, err = NewAI(opts)
	} else {
		adp, err = New(opts)
	}
	if err != nil {
		t.Fatal(err)
	}
	a := adp.(*Adapter)
	a.oauthHost = srv.URL
	a.apiBase = srv.URL + "/coding/v1"
	a.pollInterval = time.Millisecond
	a.httpClient = &http.Client{Timeout: 5 * time.Second}
	return a
}
