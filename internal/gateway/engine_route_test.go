package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/localruntime"
)

func TestLoadingLocalExtendsPreludeOnly(t *testing.T) {
	gw, err := New(config.Config{SchemaVersion: 1}, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.models = []catalog.Model{{
		ID: "llama", Tier: catalog.TierLocal, Status: string(localruntime.StateLoading),
	}}
	if got := gw.streamPrelude("llama"); got != 30*time.Second {
		t.Fatalf("loading prelude = %s", got)
	}
	if got := gw.streamPrelude("cloud"); got != 5*time.Second {
		t.Fatalf("other prelude = %s", got)
	}
	if gw.cfg.RequestDeadline() != 2*time.Minute {
		t.Fatalf("deadline = %s", gw.cfg.RequestDeadline())
	}
}

func TestExactNativeUnknownPassesThrough(t *testing.T) {
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "ok"), countOK(new(int), "other"))
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`)
	if _, _, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if hits == 0 {
		t.Fatal("exact model with tools did not reach upstream")
	}
}

func TestRequiredToolsFilterCandidates(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	body := []byte(`{"model":"pea/auto","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`)
	if _, _, err := gw.Chat(context.Background(), body); err == nil || !strings.Contains(err.Error(), "no eligible") {
		t.Fatalf("error = %v", err)
	}
}

func TestLocalExactNeverCloudFallback(t *testing.T) {
	localHits, cloudHits := 0, 0
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "same"}}})
			return
		}
		localHits++
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(local.Close)
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "same"}}})
			return
		}
		cloudHits++
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "cloud"}}}})
	}))
	t.Cleanup(cloud.Close)
	cfg := config.Config{
		SchemaVersion: 1,
		Providers: []config.Provider{
			{ID: "local", Adapter: "openai_compat", Tier: "local", BaseURL: local.URL + "/v1"},
			{ID: "cloud", Adapter: "openai_compat", Tier: "paid", BaseURL: cloud.URL + "/v1"},
		},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	_, _, err = gw.Chat(context.Background(), []byte(`{"model":"same","messages":[{"role":"user","content":"hi"}]}`))
	if cloudHits != 0 {
		t.Fatalf("cloud hits = %d, local hits = %d, err = %v", cloudHits, localHits, err)
	}
	if localHits == 0 {
		t.Fatal("local account was not tried")
	}
}

func TestAutomaticSkipsCooledAccount(t *testing.T) {
	hitsA, hitsB := 0, 0
	gw := twoAccountGateway(t, countOK(&hitsA, "a"), countOK(&hitsB, "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.mu.Lock()
	gw.cool["acct-a"] = Cooldown{AccountID: "acct-a", Until: time.Now().Add(time.Hour)}
	gw.mu.Unlock()
	_, account, err := gw.Chat(context.Background(), []byte(`{"model":"pea/auto","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if account != "acct-b" || hitsA != 0 || hitsB == 0 {
		t.Fatalf("account=%s hits a=%d b=%d", account, hitsA, hitsB)
	}
}

func TestAutomaticSessionStable(t *testing.T) {
	hitsA, hitsB := 0, 0
	gw := twoAccountGateway(t, countOK(&hitsA, "a"), countOK(&hitsB, "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	body := []byte(`{"model":"pea/auto","session_id":"sess-1","messages":[{"role":"user","content":"hi"}]}`)
	if _, account, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatal(err)
	} else if account == "" {
		t.Fatal("missing account")
	}
	firstA, firstB := hitsA, hitsB
	if _, _, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if hitsA-firstA > 0 && hitsB-firstB > 0 {
		t.Fatalf("session moved accounts: a %d b %d", hitsA, hitsB)
	}
}

func TestFreeRouteUsesVerifiedZeroOnly(t *testing.T) {
	freeHits, paidHits := 0, 0
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "priced"}}})
				return
			}
			paidHits++
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "paid"}}}})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gratis"}}})
				return
			}
			freeHits++
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "free"}}}})
		},
	)
	zero := 0.0
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"gratis": {Input: &zero, Output: &zero, Verified: true},
		"priced": {Input: &zero, Output: &zero, Verified: false},
	}
	resp, _, err := gw.Chat(context.Background(), []byte(`{"model":"pea/free","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if freeHits != 1 || paidHits != 0 || !strings.Contains(string(resp.Raw), "free") {
		t.Fatalf("free=%d paid=%d raw=%s", freeHits, paidHits, resp.Raw)
	}
}

func TestResponseCacheIsOptIn(t *testing.T) {
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "ok"), countOK(new(int), "other"))
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if _, _, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	before := hits
	gw.cfg.RequestEngine.CacheResponses = true
	if _, _, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if hits-before != 1 {
		t.Fatalf("cached upstream calls = %d", hits-before)
	}
}

func TestExactModelNeverSubstituted(t *testing.T) {
	var seen string
	gw := twoAccountGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "exact-model"}}})
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = body.Model
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
	}, countOK(new(int), "other"))
	gw.cfg.AutomaticRoutes.Enabled = true
	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"exact-model","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatal(err)
	}
	if seen != "exact-model" {
		t.Fatalf("upstream model = %q", seen)
	}
}

func TestRetryAfterRespected(t *testing.T) {
	gw := twoAccountGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"quota"}`)
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
	})
	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatal(err)
	}
	var waited time.Duration
	for _, cool := range gw.Cooldowns() {
		if cool.RemainingMs > waited.Milliseconds() {
			waited = time.Duration(cool.RemainingMs) * time.Millisecond
		}
	}
	if waited < 10*time.Second || waited > 20*time.Second {
		t.Fatalf("retry-after cooldown = %s", waited)
	}
}

func TestAdaptiveRecordsFailedAccount(t *testing.T) {
	gw := twoAccountGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}, countOK(new(int), "ok"))
	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatal(err)
	}
	gw.mu.Lock()
	stat := gw.accountStats["acct-a"]
	gw.mu.Unlock()
	if !stat.Known || stat.Errors == 0 {
		t.Fatalf("stat = %+v", stat)
	}
}

func TestInFlightChatCoalescesEligibleCalls(t *testing.T) {
	var hits int
	var mu sync.Mutex
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		mu.Lock()
		hits++
		mu.Unlock()
		once.Do(func() { close(entered) })
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
	}))
	t.Cleanup(upstream.Close)
	cfg := config.Config{
		SchemaVersion: 1,
		Providers: []config.Provider{{
			ID: "only", Adapter: "openai_compat", Tier: "paid", BaseURL: upstream.URL + "/v1",
		}},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	gw.cfg.RequestEngine.CacheResponses = true
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	var wg sync.WaitGroup
	wg.Add(2)
	errCh := make(chan error, 2)
	go func() {
		defer wg.Done()
		_, _, err := gw.Chat(context.Background(), body)
		errCh <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not reach upstream")
	}
	go func() {
		defer wg.Done()
		_, _, err := gw.Chat(context.Background(), body)
		errCh <- err
	}()
	time.Sleep(150 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("upstream hits = %d, want 1 shared call", hits)
	}
}

func TestRefreshDropsRemovedModelCache(t *testing.T) {
	var mu sync.Mutex
	modelID := "m"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			mu.Lock()
			id := modelID
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": id}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
	}))
	t.Cleanup(upstream.Close)
	cfg := config.Config{
		SchemaVersion: 1,
		Providers: []config.Provider{{
			ID: "only", Adapter: "openai_compat", Tier: "paid", BaseURL: upstream.URL + "/v1",
		}},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	gw.cfg.RequestEngine.CacheResponses = true
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if _, _, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	endpoint := upstream.URL + "/v1"
	if _, ok := gw.cachedChat("only", endpoint, "m", body); !ok {
		t.Fatal("expected a cached response")
	}
	mu.Lock()
	modelID = "other"
	mu.Unlock()
	gw.Refresh(context.Background())
	if _, ok := gw.cachedChat("only", endpoint, "m", body); ok {
		t.Fatal("cached response survived model removal")
	}
}

func TestRemoveProviderDropsCachedResponses(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.RequestEngine.CacheResponses = true
	raw := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	gw.storeChat("acct-a", "http://example", "m", raw, []byte(`{"cached":true}`))
	if _, ok := gw.cachedChat("acct-a", "http://example", "m", raw); !ok {
		t.Fatal("cache miss before removal")
	}
	if err := gw.RemoveProvider(context.Background(), "acct-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := gw.cachedChat("acct-a", "http://example", "m", raw); ok {
		t.Fatal("cached response survived account removal")
	}
}

func TestDisabledAutomaticRouteDoesNotMatchAllAccounts(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"pea/auto","messages":[{"role":"user","content":"hi"}]}`))
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("error = %v", err)
	}
}
