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

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/localruntime"
	"github.com/ks1686/peaproxy/internal/router"
)

func TestLoadingLocalExtendsPreludeOnly(t *testing.T) {
	gw, err := New(config.Config{SchemaVersion: 1}, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.cfg.RequestEngine.PreludeTimeout = "5s"
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

// A deployment that cannot do the tools a request needs is excluded. The stub
// matters: openai_compat declares tool support, so without one this test would
// pass for the wrong reason. It used to, and was pinning a bug -- see D9.
func TestRequiredToolsFilterCandidates(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	for i := range gw.inst {
		gw.inst[i].Adapter = noTools{Adapter: gw.inst[i].Adapter}
	}
	gw.cfg.AutomaticRoutes.Enabled = true
	body := []byte(`{"model":"pea/auto","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`)
	if _, _, err := gw.Chat(context.Background(), body); err == nil || !strings.Contains(err.Error(), "no eligible") {
		t.Fatalf("error = %v", err)
	}
}

// noTools is a deployment that cannot run tool calls. It states the refusal
// through UnsupportedRequirements rather than only setting Tools: false, because
// a false bool and an omitted field are the same value -- see
// adapter.UnsupportedRequirements.
type noTools struct{ adapter.Adapter }

func (n noTools) UnsupportedRequirements() []catalog.Requirement {
	return []catalog.Requirement{catalog.RequirementTools}
}

func (n noTools) Capabilities() adapter.Capabilities {
	c := n.Adapter.Capabilities()
	c.Tools = false
	return c
}

// A tool-using request must reach an OpenAI-compatible provider. Before
// openai_compat declared Tools, every such request on an automatic route failed
// with "no eligible model", because the whole compatible roster was excluded
// from tool-aware routing.
func TestToolUsingRequestReachesAnOpenAICompatibleProvider(t *testing.T) {
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "ok"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	body := []byte(`{"model":"pea/auto","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`)
	if _, _, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatalf("a tool-using request on an automatic route failed: %v", err)
	}
	if hits == 0 {
		t.Fatal("no upstream received the tool-using request")
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
	gw.cool["acct-a"] = cooldownSlots{wide: Cooldown{AccountID: "acct-a", Until: time.Now().Add(time.Hour)}}
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

func slowStream(delay time.Duration, hits *int, mu *sync.Mutex) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		mu.Lock()
		*hits++
		mu.Unlock()
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pea\"}}]}\n\ndata: [DONE]\n\n")
	}
}

func TestCooldownErrorNamesAccountAndCause(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.mu.Lock()
	for _, id := range []string{"acct-a", "acct-b"} {
		gw.cool[id] = cooldownSlots{wide: Cooldown{AccountID: id, Until: time.Now().Add(time.Minute), Reason: "HTTP 429 (rate-limit)"}}
	}
	gw.mu.Unlock()
	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if err == nil {
		t.Fatal("expected cooldown error")
	}
	for _, want := range []string{"all matching accounts in cooldown", "acct-a after HTTP 429 (rate-limit)", "acct-b after HTTP 429 (rate-limit)", "s left"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestEdgeTransport503IsRetriedAndCooledBriefly(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		mu.Lock()
		hits++
		first := hits == 1
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "upstream connect error or disconnect/reset before headers. reset reason: connection timeout")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pea\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	cfg := config.Config{SchemaVersion: 1, Providers: []config.Provider{
		{ID: "only", Adapter: "openai_compat", Tier: "paid", BaseURL: srv.URL + "/v1"},
	}}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	var out strings.Builder
	if _, err := gw.ChatStream(context.Background(), []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`), &out); err != nil {
		t.Fatalf("one edge connection timeout failed the request: %v", err)
	}
	if !strings.Contains(out.String(), "pea") {
		t.Fatalf("out=%q", out.String())
	}

	gw.markCooldown("only", "m", adapter.HTTPError{Status: http.StatusServiceUnavailable, Body: "upstream connect error or disconnect/reset before headers. reset reason: connection timeout"})
	for _, c := range gw.Cooldowns() {
		if c.RemainingMs > 5000 {
			t.Fatalf("edge transport failure cooled the account for %dms, want at most 5s", c.RemainingMs)
		}
	}
}

func TestCooldownRetryAfterFollowsUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	t.Cleanup(srv.Close)
	cfg := config.Config{SchemaVersion: 1, Providers: []config.Provider{
		{ID: "only", Adapter: "openai_compat", Tier: "paid", BaseURL: srv.URL + "/v1"},
	}}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	body := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	_, err = gw.ChatStream(context.Background(), body, io.Discard)
	if got := router.RetryAfterSeconds(err); got != 2 {
		t.Fatalf("first failure Retry-After = %d, want the upstream's 2 (err=%v)", got, err)
	}
	_, err = gw.ChatStream(context.Background(), body, io.Discard)
	if got := router.RetryAfterSeconds(err); got < 1 || got > 2 {
		t.Fatalf("cooled follow-up Retry-After = %d, want <= 2 (err=%v)", got, err)
	}
}

func TestSlowPreludeOnOnlyAccountStreams(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(slowStream(200*time.Millisecond, &hits, &mu))
	t.Cleanup(srv.Close)
	cfg := config.Config{SchemaVersion: 1, Providers: []config.Provider{
		{ID: "only", Adapter: "openai_compat", Tier: "paid", BaseURL: srv.URL + "/v1"},
	}}
	cfg.RequestEngine.PreludeTimeout = "20ms"
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	var out strings.Builder
	if _, err := gw.ChatStream(context.Background(), []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`), &out); err != nil {
		t.Fatalf("slow first event on the only account failed: %v", err)
	}
	if !strings.Contains(out.String(), "pea") || hits != 1 {
		t.Fatalf("hits=%d out=%q", hits, out.String())
	}
	if len(gw.Cooldowns()) != 0 {
		t.Fatalf("slow prelude cooled the account: %+v", gw.Cooldowns())
	}
}

func TestSlowPreludeFailsOverWithoutCooldown(t *testing.T) {
	var mu sync.Mutex
	hitsA, hitsB := 0, 0
	gw := twoAccountGateway(t, slowStream(time.Second, &hitsA, &mu), slowStream(0, &hitsB, &mu))
	gw.cfg.Failover.Policy = "fill-first"
	gw.cfg.RequestEngine.PreludeTimeout = "20ms"
	var out strings.Builder
	account, err := gw.ChatStream(context.Background(), []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`), &out)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if account != "acct-b" || hitsA != 1 || hitsB != 1 {
		t.Fatalf("account=%s hits a=%d b=%d", account, hitsA, hitsB)
	}
	if len(gw.Cooldowns()) != 0 {
		t.Fatalf("slow prelude cooled the account: %+v", gw.Cooldowns())
	}
}
