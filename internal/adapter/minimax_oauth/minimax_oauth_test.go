package minimax_oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/oauth"
)

func TestDeviceLoginRefreshAndMessages(t *testing.T) {
	var polls int
	var mu sync.Mutex
	var gotKey string
	var gotPath string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/device/code":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("scope") != Scope ||
				r.PostForm.Get("code_challenge_method") != "S256" || r.PostForm.Get("code_challenge") == "" ||
				r.PostForm.Get("state") == "" || r.PostForm.Get("response_type") != "" {
				t.Errorf("device form %#v", r.PostForm)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"base_resp":{"status_code":0},"user_code":"MM-CODE","verification_uri":"https://platform.minimax.io/device","expired_in":`+unixMS(time.Now().Add(time.Minute))+`,"interval":3000,"state":"`+r.PostForm.Get("state")+`"}`)
		case "/oauth2/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			w.Header().Set("Content-Type", "application/json")
			if r.PostForm.Get("grant_type") == "refresh_token" {
				if r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("refresh_token") != "refresh-1" {
					t.Errorf("refresh form %#v", r.PostForm)
				}
				_, _ = io.WriteString(w, `{"status":"success","access_token":"access-2","refresh_token":"refresh-2","expired_in":`+unixMS(time.Now().Add(time.Hour))+`,"resource_url":"`+srv.URL+`"}`)
				return
			}
			if r.PostForm.Get("grant_type") != oauth.DeviceGrantType || r.PostForm.Get("user_code") != "MM-CODE" || r.PostForm.Get("code_verifier") == "" {
				t.Errorf("poll form %#v", r.PostForm)
			}
			mu.Lock()
			polls++
			n := polls
			mu.Unlock()
			if n == 1 {
				_, _ = io.WriteString(w, `{"status":"pending"}`)
				return
			}
			_, _ = io.WriteString(w, `{"status":"success","access_token":"access-1","refresh_token":"refresh-1","expired_in":3600,"resource_url":"`+srv.URL+`"}`)
		case "/anthropic/v1/models":
			mu.Lock()
			gotKey = r.Header.Get("x-api-key")
			gotPath = r.URL.Path
			mu.Unlock()
			if r.Header.Get("anthropic-version") == "" {
				t.Error("missing anthropic-version")
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"MiniMax-M3","display_name":"M3"}]}`)
		case "/anthropic/v1/messages":
			mu.Lock()
			gotKey = r.Header.Get("x-api-key")
			gotPath = r.URL.Path
			mu.Unlock()
			_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"MiniMax-M3","stop_reason":"end_turn"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	raw, err := New(adapter.Options{ID: "mm"})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Adapter)
	a.codeURL = srv.URL + "/oauth2/device/code"
	a.tokenURL = srv.URL + "/oauth2/token"
	a.httpClient = srv.Client()
	a.pollInterval = time.Millisecond

	sess, err := a.AuthStart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sess.UserCode != "MM-CODE" {
		t.Fatalf("session %#v", sess)
	}
	if err := a.AuthComplete(context.Background(), sess, ""); err != nil {
		t.Fatal(err)
	}
	if a.token.ExtraGet(resourceExtra) != srv.URL {
		t.Fatalf("resource %q", a.token.ExtraGet(resourceExtra))
	}
	if !a.token.ExpiresAt.After(time.Now().Add(30*time.Minute)) || !a.token.ExpiresAt.Before(time.Now().Add(2*time.Hour)) {
		t.Fatalf("ttl expiry %s", a.token.ExpiresAt)
	}

	resp, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "MiniMax-M3", Messages: []adapter.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" || gotKey != "access-1" || gotPath != "/anthropic/v1/messages" {
		t.Fatalf("content %q key %q path %q", resp.Content, gotKey, gotPath)
	}

	a.mu.Lock()
	a.token.ExpiresAt = time.Now().Add(time.Minute)
	a.mu.Unlock()
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "MiniMax-M3" || !models[0].SubscriptionOAuth || models[0].Provider != Name || models[0].AccountID != "mm" {
		t.Fatalf("models %#v", models)
	}
	if gotKey != "access-2" {
		t.Fatalf("refreshed key %q", gotKey)
	}
	if a.token.RefreshToken != "refresh-2" || a.token.ExtraGet(resourceExtra) != srv.URL {
		t.Fatalf("token after refresh refresh=%q resource=%q", a.token.RefreshToken, a.token.ExtraGet(resourceExtra))
	}
}

func TestStateMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"user_code":"X","verification_uri":"https://platform.minimax.io/device","expired_in":60000,"interval":3000,"state":"other"}`)
	}))
	t.Cleanup(srv.Close)
	raw, err := New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Adapter)
	a.codeURL = srv.URL
	a.httpClient = srv.Client()
	_, err = a.AuthStart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatal(err)
	}
}

func TestRefreshRejects4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	raw, err := New(adapter.Options{ID: "mm", OAuth: oauth.Token{
		AccessToken:  "old",
		RefreshToken: "spent",
		ExpiresAt:    time.Now().Add(time.Minute),
	}})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Adapter)
	a.tokenURL = srv.URL
	a.httpClient = srv.Client()
	_, err = a.ListModels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sign in again") {
		t.Fatal(err)
	}
}

func TestInferenceBase(t *testing.T) {
	if got := inferenceBase(""); got != InferenceBase {
		t.Fatalf("empty %s", got)
	}
	if got := inferenceBase("https://api.minimax.io"); got != "https://api.minimax.io/anthropic" {
		t.Fatalf("host %s", got)
	}
	if got := inferenceBase("https://api.minimax.io/anthropic"); got != "https://api.minimax.io/anthropic" {
		t.Fatalf("already %s", got)
	}
	if got := inferenceBase("https://api.minimax.io/anthropic/v1"); got != "https://api.minimax.io/anthropic/v1" {
		t.Fatalf("v1 %s", got)
	}
}

func TestExpiryFrom(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	abs := time.UnixMilli(1_700_000_000_000)
	if got := expiryFrom(1_700_000_000_000, now); !got.Equal(abs) {
		t.Fatalf("unix ms %s", got)
	}
	if got := expiryFrom(90, now); !got.Equal(now.Add(90 * time.Second)) {
		t.Fatalf("ttl %s", got)
	}
	if !expiryFrom(0, now).IsZero() {
		t.Fatal("zero")
	}
}

func TestNon200PollIsNotPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "token") || r.URL.Path == "/" {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.PostForm.Get("grant_type") == oauth.DeviceGrantType {
				http.Error(w, "nope", http.StatusBadRequest)
				return
			}
		}
		_ = r.ParseForm()
		state := r.PostForm.Get("state")
		_, _ = io.WriteString(w, `{"user_code":"U","verification_uri":"https://platform.minimax.io/d","expired_in":60000,"interval":1000,"state":"`+state+`"}`)
	}))
	t.Cleanup(srv.Close)
	raw, err := New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Adapter)
	a.codeURL = srv.URL
	a.tokenURL = srv.URL
	a.httpClient = srv.Client()
	a.pollInterval = time.Millisecond
	if _, err := a.AuthStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	err = a.AuthComplete(context.Background(), adapter.AuthSession{}, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatal(err)
	}
}

func unixMS(t time.Time) string {
	return strconv.FormatInt(t.UnixMilli(), 10)
}
