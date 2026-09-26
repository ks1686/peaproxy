package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
)

func twoAccountGateway(t *testing.T, handlerA, handlerB http.HandlerFunc) *Gateway {
	t.Helper()
	a := httptest.NewServer(handlerA)
	b := httptest.NewServer(handlerB)
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Providers: []config.Provider{
			{ID: "acct-a", Adapter: "openai_compat", Tier: "paid", BaseURL: a.URL + "/v1"},
			{ID: "acct-b", Adapter: "openai_compat", Tier: "paid", BaseURL: b.URL + "/v1"},
		},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	return gw
}

func TestChatFailsover429ToNextAccount(t *testing.T) {
	hitsA, hitsB := 0, 0
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gpt-4o"}}})
				return
			}
			hitsA++
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"quota"}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gpt-4o"}}})
				return
			}
			hitsB++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "from-b"}}},
			})
		},
	)
	resp, account, err := gw.Chat(context.Background(), []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "from-b" {
		t.Fatalf("content %q account %s", resp.Content, account)
	}
	if hitsA == 0 || hitsB == 0 {
		t.Fatalf("expected both accounts, a=%d b=%d", hitsA, hitsB)
	}
	cds := gw.Cooldowns()
	if len(cds) != 1 || cds[0].AccountID != "acct-a" {
		t.Fatalf("cooldown: %#v", cds)
	}
}

func TestChatFailsover401(t *testing.T) {
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "bad key")
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
			})
		},
	)
	resp, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err != nil || resp.Content != "ok" {
		t.Fatalf("%v %#v", err, resp)
	}
}

func TestNonRetryableDoesNotFailover(t *testing.T) {
	hitsB := 0
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "nope")
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hitsB++
			_ = json.NewEncoder(w).Encode(map[string]any{})
		},
	)
	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if hitsB != 0 {
		t.Fatal("must not failover on 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("%v", err)
	}
}

func TestInstancesRedactOAuthTokens(t *testing.T) {
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Providers: []config.Provider{{
			ID:      "anthropic-oauth",
			Adapter: "anthropic_oauth",
			Tier:    "paid",
			OAuth: &config.OAuthToken{
				AccessToken:  "secret-at",
				RefreshToken: "secret-rt",
				Email:        "a@b.c",
			},
		}},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	got := gw.Instances()
	if len(got) != 1 {
		t.Fatalf("%#v", got)
	}
	if got[0].OAuth == nil || got[0].OAuth.AccessToken != "configured" {
		t.Fatalf("access token leaked: %#v", got[0].OAuth)
	}
	if got[0].OAuth.RefreshToken != "" {
		t.Fatalf("refresh leaked: %#v", got[0].OAuth)
	}
	if got[0].OAuth.Email != "a@b.c" {
		t.Fatalf("email %s", got[0].OAuth.Email)
	}
}
