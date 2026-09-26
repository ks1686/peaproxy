package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/router"
)

func twoAccountGateway(t *testing.T, handlerA, handlerB http.HandlerFunc) *Gateway {
	t.Helper()
	return policyGateway(t, "", handlerA, handlerB)
}

func policyGateway(t *testing.T, policy string, handlers ...http.HandlerFunc) *Gateway {
	t.Helper()
	if len(handlers) < 2 {
		t.Fatal("need at least two handlers")
	}
	ids := []string{"acct-a", "acct-b", "acct-c"}
	var providers []config.Provider
	for i, h := range handlers {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		providers = append(providers, config.Provider{
			ID: ids[i], Adapter: "openai_compat", Tier: "paid", BaseURL: srv.URL + "/v1",
		})
	}
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Failover:      config.FailoverPrefs{Policy: policy},
		Providers:     providers,
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	return gw
}

func countOK(hits *int, content string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		*hits++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
		})
	}
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
	// Cooled account must not be re-hit (cooldown storm).
	aBefore, bBefore := hitsA, hitsB
	resp2, _, err := gw.Chat(context.Background(), []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || resp2.Content != "from-b" {
		t.Fatalf("second chat %v %#v", err, resp2)
	}
	if hitsA != aBefore {
		t.Fatalf("cooldown storm: acct-a hit again a=%d→%d", aBefore, hitsA)
	}
	if hitsB <= bBefore {
		t.Fatalf("expected hot account retry, b=%d→%d", bBefore, hitsB)
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

func TestResponsesTranslatesToChatCompletions(t *testing.T) {
	var chatBody []byte
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/v1/models":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "llama3.2"}}})
			case r.URL.Path == "/v1/chat/completions":
				chatBody, _ = io.ReadAll(r.Body)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":    "chatcmpl-x",
					"model": "llama3.2",
					"choices": []map[string]any{{
						"message": map[string]string{"role": "assistant", "content": "pong"},
					}},
				})
			default:
				http.NotFound(w, r)
			}
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "other"}}})
				return
			}
			http.Error(w, "unused", http.StatusInternalServerError)
		},
	)
	out, account, err := gw.Responses(context.Background(), []byte(`{"model":"llama3.2","input":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	if account == "" {
		t.Fatal("missing account")
	}
	if !strings.Contains(string(chatBody), `"messages"`) || strings.Contains(string(chatBody), `"input"`) {
		t.Fatalf("upstream should receive chat completions: %s", chatBody)
	}
	if !strings.Contains(string(out), `"object":"response"`) || !strings.Contains(string(out), `"output_text":"pong"`) {
		t.Fatalf("responses wrap: %s", out)
	}
}

func TestChatAll429DoesNotStormCooledAccounts(t *testing.T) {
	hitsA, hitsB := 0, 0
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hitsA++
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"quota-a"}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hitsB++
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"quota-b"}`)
		},
	)
	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err == nil {
		t.Fatal("expected cooldown error")
	}
	var ce router.CooldownError
	if !errors.As(err, &ce) {
		t.Fatalf("want CooldownError, got %T %v", err, err)
	}
	if hitsA == 0 || hitsB == 0 {
		t.Fatalf("first pass a=%d b=%d", hitsA, hitsB)
	}
	a1, b1 := hitsA, hitsB
	_, _, err = gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if !errors.As(err, &ce) {
		t.Fatalf("second: %v", err)
	}
	if hitsA != a1 || hitsB != b1 {
		t.Fatalf("cooldown storm a %d→%d b %d→%d", a1, hitsA, b1, hitsB)
	}
}

func TestChatFailsover401Then429ThenSucceeds(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "bad key")
	}))
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "quota")
	}))
	c := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "from-c"}}},
		})
	}))
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	t.Cleanup(c.Close)
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Providers: []config.Provider{
			{ID: "acct-a", Adapter: "openai_compat", Tier: "paid", BaseURL: a.URL + "/v1"},
			{ID: "acct-b", Adapter: "openai_compat", Tier: "paid", BaseURL: b.URL + "/v1"},
			{ID: "acct-c", Adapter: "openai_compat", Tier: "paid", BaseURL: c.URL + "/v1"},
		},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	resp, account, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err != nil || resp.Content != "from-c" {
		t.Fatalf("%v %#v account=%s", err, resp, account)
	}
	ids := map[string]bool{}
	for _, cd := range gw.Cooldowns() {
		ids[cd.AccountID] = true
	}
	if !ids["acct-a"] || !ids["acct-b"] {
		t.Fatalf("expected 401 and 429 cooldowns: %#v", gw.Cooldowns())
	}
}

func TestResponsesFailsover429(t *testing.T) {
	hitsA := 0
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "llama3.2"}}})
				return
			}
			hitsA++
			w.WriteHeader(http.StatusTooManyRequests)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "llama3.2"}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
			})
		},
	)
	out, _, err := gw.Responses(context.Background(), []byte(`{"model":"llama3.2","input":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	if hitsA == 0 {
		t.Fatal("expected first account 429")
	}
	if !strings.Contains(string(out), `"output_text":"ok"`) {
		t.Fatalf("%s", out)
	}
}

func TestListedAppliesPinAndRenameWithoutChangingRouteID(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "alpha"}, {"id": "beta"}},
		})
	}))
	t.Cleanup(up.Close)
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Catalog: config.CatalogPrefs{
			Pin:    []string{"beta"},
			Rename: map[string]string{"beta": "Beta local"},
		},
		Hide: config.HideList{Models: []string{"beta"}},
		Providers: []config.Provider{{
			ID: "local", Adapter: "openai_compat", Tier: "local", BaseURL: up.URL + "/v1",
		}},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	listed := gw.Listed("all")
	for _, m := range listed {
		if m.ID == "beta" {
			t.Fatal("hidden pinned model leaked into listing")
		}
	}
	ann := gw.Annotated("all")
	if len(ann) < 2 || ann[0].ID != "beta" || !ann[0].Pinned || ann[0].DisplayName != "Beta local" {
		t.Fatalf("annotated pin/rename: %#v", ann)
	}
	if _, ok := catalog.FindRoutable(gw.Models(), gw.Query(), "beta"); !ok {
		t.Fatal("hidden renamed model must still route by live id")
	}
}

func TestAdapterHealthAndCooldownRemaining(t *testing.T) {
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"quota"}`)
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
	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`)); err != nil {
		t.Fatal(err)
	}
	cds := gw.Cooldowns()
	if len(cds) != 1 || cds[0].RemainingMs <= 0 || cds[0].RemainingMs > 30_000 {
		t.Fatalf("remaining: %#v", cds)
	}
	health := gw.AdapterHealth()
	if len(health) != 2 {
		t.Fatalf("health: %#v", health)
	}
	foundCool := false
	for _, h := range health {
		switch h.Status {
		case "ok":
			if h.Models < 1 {
				t.Fatalf("%#v", h)
			}
		case "cooldown":
			foundCool = true
		default:
			t.Fatalf("adapter health: %#v", h)
		}
	}
	if !foundCool {
		t.Fatal("expected cooldown overlay on adapter health")
	}
}

func TestRoundRobinRotatesStartingAccount(t *testing.T) {
	hitsA, hitsB := 0, 0
	gw := policyGateway(t, "round-robin", countOK(&hitsA, "from-a"), countOK(&hitsB, "from-b"))
	for i := 0; i < 2; i++ {
		if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`)); err != nil {
			t.Fatal(err)
		}
	}
	if hitsA != 1 || hitsB != 1 {
		t.Fatalf("round-robin should split starts a=%d b=%d", hitsA, hitsB)
	}
}

func TestFillFirstAlwaysStartsAtFirstHotAccount(t *testing.T) {
	hitsA, hitsB := 0, 0
	gw := policyGateway(t, "fill-first", countOK(&hitsA, "from-a"), countOK(&hitsB, "from-b"))
	for i := 0; i < 3; i++ {
		resp, account, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`))
		if err != nil || resp.Content != "from-a" || account != "acct-a" {
			t.Fatalf("fill-first %d: %v %#v account=%s", i, err, resp, account)
		}
	}
	if hitsA != 3 || hitsB != 0 {
		t.Fatalf("fill-first must not rotate a=%d b=%d", hitsA, hitsB)
	}
}

func TestStickyStaysOnLastSuccessAfterCooldownExpires(t *testing.T) {
	old := cooldownTTL
	cooldownTTL = 20 * time.Millisecond
	t.Cleanup(func() { cooldownTTL = old })

	hitsA, hitsB := 0, 0
	gw := policyGateway(t,
		"sticky",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hitsA++
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"quota"}`)
		},
		countOK(&hitsB, "from-b"),
	)
	resp, account, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err != nil || resp.Content != "from-b" || account != "acct-b" {
		t.Fatalf("first sticky: %v %#v account=%s", err, resp, account)
	}
	if hitsA != 1 || hitsB != 1 {
		t.Fatalf("first pass a=%d b=%d", hitsA, hitsB)
	}
	cds := gw.Cooldowns()
	if len(cds) != 1 || cds[0].AccountID != "acct-a" || cds[0].RemainingMs <= 0 {
		t.Fatalf("cooldown under sticky: %#v", cds)
	}
	time.Sleep(40 * time.Millisecond)
	aBefore, bBefore := hitsA, hitsB
	resp, account, err = gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err != nil || resp.Content != "from-b" || account != "acct-b" {
		t.Fatalf("second sticky: %v %#v account=%s", err, resp, account)
	}
	if hitsA != aBefore {
		t.Fatalf("sticky should not return to acct-a after cooldown a=%d→%d", aBefore, hitsA)
	}
	if hitsB <= bBefore {
		t.Fatalf("expected sticky retry on b %d→%d", bBefore, hitsB)
	}
}

func TestFillFirstReturnsToPrimaryAfterCooldownExpires(t *testing.T) {
	old := cooldownTTL
	cooldownTTL = 20 * time.Millisecond
	t.Cleanup(func() { cooldownTTL = old })

	hitsA, hitsB := 0, 0
	aOK := false
	gw := policyGateway(t,
		"fill-first",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hitsA++
			if !aOK {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":"quota"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "from-a"}}},
			})
		},
		countOK(&hitsB, "from-b"),
	)
	if _, account, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`)); err != nil || account != "acct-b" {
		t.Fatalf("failover: %v account=%s", err, account)
	}
	time.Sleep(40 * time.Millisecond)
	aOK = true
	resp, account, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err != nil || resp.Content != "from-a" || account != "acct-a" {
		t.Fatalf("fill-first should resume primary: %v %#v account=%s", err, resp, account)
	}
	if hitsA < 2 {
		t.Fatalf("expected acct-a retried after cooldown, hits=%d", hitsA)
	}
}

func TestChatFailsoverOnRateLimitErrorBody(t *testing.T) {
	hitsB := 0
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"quota","api_key":"sk-secret-live"}}`)
		},
		countOK(&hitsB, "ok"),
	)
	resp, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err != nil || resp.Content != "ok" {
		t.Fatalf("%v %#v", err, resp)
	}
	if hitsB == 0 {
		t.Fatal("expected failover from rate-limit body")
	}
	cds := gw.Cooldowns()
	if len(cds) != 1 || cds[0].AccountID != "acct-a" {
		t.Fatalf("cooldown: %#v", cds)
	}
	if strings.Contains(cds[0].Reason, "sk-secret") || strings.Contains(cds[0].Reason, "api_key") {
		t.Fatalf("cooldown reason leaked secret: %#v", cds[0])
	}
	if !strings.Contains(strings.ToLower(cds[0].Reason), "rate") {
		t.Fatalf("want rate-limit reason, got %q", cds[0].Reason)
	}
}

func TestChatFailsoverOnOverloadedAndAuthBodies(t *testing.T) {
	hits := 0
	gw := policyGateway(t, "fill-first",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"type":"overloaded_error"}}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hits++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "from-c"}}},
			})
		},
	)
	resp, account, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err != nil || resp.Content != "from-c" || account != "acct-c" {
		t.Fatalf("%v %#v account=%s", err, resp, account)
	}
	if hits != 1 {
		t.Fatalf("hits c=%d", hits)
	}
	ids := map[string]string{}
	for _, cd := range gw.Cooldowns() {
		ids[cd.AccountID] = cd.Reason
	}
	if _, ok := ids["acct-a"]; !ok {
		t.Fatalf("missing overloaded cooldown: %#v", gw.Cooldowns())
	}
	if _, ok := ids["acct-b"]; !ok {
		t.Fatalf("missing auth cooldown: %#v", gw.Cooldowns())
	}
	for id, reason := range ids {
		if strings.Contains(reason, "x-api-key") || strings.Contains(reason, "sk-") {
			t.Fatalf("%s reason leaked: %q", id, reason)
		}
	}
}

func TestNonRetryableBodyDoesNotFailover(t *testing.T) {
	hitsB := 0
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"messages must be an array"}}`)
		},
		countOK(&hitsB, "nope"),
	)
	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if hitsB != 0 {
		t.Fatal("must not failover on invalid_request_error")
	}
}
