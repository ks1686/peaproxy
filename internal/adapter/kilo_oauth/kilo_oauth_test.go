package kilo_oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/oauth"
)

func TestDeviceLoginThenChatUsesOpenRouter(t *testing.T) {
	var polls int
	var sawAuth, sawEditor, sawUA bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/device-auth/codes":
			body, _ := io.ReadAll(r.Body)
			if len(body) != 0 {
				t.Errorf("device start body = %q, want empty", body)
			}
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("content-type %s", r.Header.Get("Content-Type"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"poll-me","user_code":"ABCD-EF","verificationUrl":"https://app.kilo.ai/device","expiresIn":600}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/device-auth/codes/poll-me":
			polls++
			w.Header().Set("Content-Type", "application/json")
			if polls == 1 {
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"status":"pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"approved","token":"kilo-token","userEmail":"a@kilo.ai"}`))
		case r.URL.Path == "/api/openrouter/models":
			if r.Header.Get("Authorization") != "Bearer kilo-token" {
				t.Errorf("models auth %s", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"kilo/test"}]}`))
		case r.URL.Path == "/api/openrouter/chat/completions":
			sawAuth = r.Header.Get("Authorization") == "Bearer kilo-token"
			sawEditor = r.Header.Get("X-Kilocode-Editorname") == editor
			sawUA = strings.HasPrefix(r.Header.Get("User-Agent"), UserAgent)
			_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	raw, err := New(adapter.Options{ID: "kilo"})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Adapter)
	a.authBase = srv.URL
	a.apiRoot = srv.URL
	a.httpClient = srv.Client()
	a.pollInterval = time.Millisecond

	sess, err := a.AuthStart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sess.UserCode != "ABCD-EF" || sess.LoginURL != "https://app.kilo.ai/device" {
		t.Fatalf("session %#v", sess)
	}
	if err := a.AuthComplete(context.Background(), sess, ""); err != nil {
		t.Fatal(err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "kilo/test" || !models[0].SubscriptionOAuth || models[0].Provider != Name {
		t.Fatalf("models %#v", models)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "kilo/test", Messages: []adapter.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" || !sawAuth || !sawEditor || !sawUA {
		t.Fatalf("content %q auth %v editor %v ua %v", resp.Content, sawAuth, sawEditor, sawUA)
	}
}

func TestDeviceStartTooManyPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	raw, err := New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Adapter)
	a.authBase = srv.URL
	a.httpClient = srv.Client()
	_, err = a.AuthStart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "too many pending") {
		t.Fatal(err)
	}
}

func TestPollDeniedAndExpired(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusForbidden, "denied"},
		{http.StatusGone, "expired"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					_, _ = w.Write([]byte(`{"code":"c","verificationUrl":"https://app.kilo.ai/device","expiresIn":60}`))
					return
				}
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(srv.Close)
			raw, err := New(adapter.Options{})
			if err != nil {
				t.Fatal(err)
			}
			a := raw.(*Adapter)
			a.authBase = srv.URL
			a.httpClient = srv.Client()
			a.pollInterval = time.Millisecond
			if _, err := a.AuthStart(context.Background()); err != nil {
				t.Fatal(err)
			}
			err = a.AuthComplete(context.Background(), adapter.AuthSession{}, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("status %d: %v", tc.status, err)
			}
		})
	}
}

func TestOpenRouterBase(t *testing.T) {
	cases := []struct {
		token string
		want  string
	}{
		{"plain", "https://api.kilo.ai/api/openrouter"},
		{"https://edge.example/api/openrouter:sekret", "https://edge.example/api/openrouter"},
		{"https://edge.example:8443:sekret", "https://edge.example:8443/api/openrouter"},
	}
	for _, tc := range cases {
		if got := openRouterBase(tc.token); got != tc.want {
			t.Errorf("openRouterBase(%q) = %s, want %s", tc.token, got, tc.want)
		}
	}
}

func TestEmbeddedTokenRoutesChat(t *testing.T) {
	token := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/openrouter/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("auth %s", got)
		}
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"edge"}}]}`))
	}))
	t.Cleanup(srv.Close)
	token = srv.URL + ":sekret"
	a := &Adapter{
		id:         "kilo",
		token:      oauth.Token{AccessToken: token},
		httpClient: srv.Client(),
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "m", Messages: []adapter.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "edge" {
		t.Fatalf("%q", resp.Content)
	}
}

func TestUserCodeFallsBackToCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"only-code","verificationUrl":"https://app.kilo.ai/device","expiresIn":30}`))
	}))
	t.Cleanup(srv.Close)
	raw, err := New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Adapter)
	a.authBase = srv.URL
	a.httpClient = srv.Client()
	sess, err := a.AuthStart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sess.UserCode != "only-code" {
		t.Fatalf("user code %q", sess.UserCode)
	}
}
