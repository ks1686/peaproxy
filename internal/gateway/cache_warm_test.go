package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/usage"
)

// PeaProxy cannot know whether a provider's cache is warm -- no API says so.
// What it can observe is that a recent call came back with cache-read tokens,
// which is evidence the deployment caches and the prefix was warm then.
func TestCacheWarmthIsObservedFromRecentCacheReads(t *testing.T) {
	now := time.Now()
	s := usage.Open("")
	s.Add(usage.Event{Time: now.Add(-time.Minute), AccountID: "acct-a", Model: "m", CacheRead: 800})
	s.Add(usage.Event{Time: now.Add(-time.Minute), AccountID: "acct-b", Model: "m", CacheRead: 0})

	warm := s.CacheWarmSince(now.Add(-10 * time.Minute))
	if !warm["acct-a\x00m"] {
		t.Fatal("a deployment that just served a cache read was not seen as warm")
	}
	if warm["acct-b\x00m"] {
		t.Fatal("a deployment with no cache read was called warm")
	}
}

// Warmth decays. An hour-old cache says little about a prefix that has grown
// since, and treating it as current would steer routing at a stale prefix.
func TestCacheWarmthExpires(t *testing.T) {
	now := time.Now()
	s := usage.Open("")
	s.Add(usage.Event{Time: now.Add(-2 * time.Hour), AccountID: "acct-a", Model: "m", CacheRead: 800})

	if s.CacheWarmSince(now.Add(-time.Hour))["acct-a\x00m"] {
		t.Fatal("a two-hour-old cache read was treated as current")
	}
	if !s.CacheWarmSince(now.Add(-3 * time.Hour))["acct-a\x00m"] {
		t.Fatal("a two-hour-old read was discarded before the window asked for it to be")
	}
}

// A cache write is not a cache hit. The first turn of a session writes a prefix
// and reads none, which is the opposite of warm.
func TestCacheWriteIsNotWarmth(t *testing.T) {
	now := time.Now()
	s := usage.Open("")
	s.Add(usage.Event{Time: now, AccountID: "acct-a", Model: "m", CacheWrite: 800})

	if s.CacheWarmSince(now.Add(-time.Minute))["acct-a\x00m"] {
		t.Fatal("a cache write was treated as a warm read")
	}
}

// A warm deployment that is genuinely more expensive must lose. A cache read
// costs a fraction of a fresh token, not nothing, so warmth is a tie-breaker and
// not an override.
func TestWarmthNeverBeatsACheaperColdDeployment(t *testing.T) {
	cheap, dear := 1.0, 9.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.models = []catalog.Model{
		{ID: "cold", AccountID: "acct-a", Price: catalog.Price{Input: &cheap, Output: &cheap, Currency: "USD", Source: "openrouter", Verified: true}},
		{ID: "warm", AccountID: "acct-a", Price: catalog.Price{Input: &dear, Output: &dear, Currency: "USD", Source: "openrouter", Verified: true}},
	}
	ranked := []instance{
		{Provider: config.Provider{ID: "acct-a"}, upstreamModel: "cold"},
		{Provider: config.Provider{ID: "acct-a"}, upstreamModel: "warm"},
	}
	warm := map[string]bool{"acct-a\x00warm": true}

	if pick := gw.preferWarm(ranked, warm); pick != 0 {
		t.Fatalf("warmth overrode a cheaper cold deployment: pick %d", pick)
	}
}

// At equal price there is nothing left to separate the candidates, so warmth is
// the deciding signal and the saving is real.
func TestWarmthBreaksAnEqualPriceTie(t *testing.T) {
	same := 2.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.models = []catalog.Model{
		{ID: "cold", AccountID: "acct-a", Price: catalog.Price{Input: &same, Output: &same, Currency: "USD", Source: "openrouter", Verified: true}},
		{ID: "warm", AccountID: "acct-a", Price: catalog.Price{Input: &same, Output: &same, Currency: "USD", Source: "openrouter", Verified: true}},
	}
	ranked := []instance{
		{Provider: config.Provider{ID: "acct-a"}, upstreamModel: "cold"},
		{Provider: config.Provider{ID: "acct-a"}, upstreamModel: "warm"},
	}

	if pick := gw.preferWarm(ranked, map[string]bool{"acct-a\x00warm": true}); pick != 1 {
		t.Fatalf("an equal-price tie was not won by warmth: pick %d", pick)
	}
}

// Warmth must not invent a comparison it cannot make. An unpriced candidate is
// not assumed expensive, so warmth declines to decide.
func TestWarmthNeverInventsAPriceForAnUnpricedCandidate(t *testing.T) {
	known := 2.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.models = []catalog.Model{
		{ID: "cold", AccountID: "acct-a", Price: catalog.Price{Input: &known, Output: &known, Currency: "USD", Source: "openrouter", Verified: true}},
		{ID: "warm", AccountID: "acct-a"}, // no published price
	}
	ranked := []instance{
		{Provider: config.Provider{ID: "acct-a"}, upstreamModel: "cold"},
		{Provider: config.Provider{ID: "acct-a"}, upstreamModel: "warm"},
	}

	if pick := gw.preferWarm(ranked, map[string]bool{"acct-a\x00warm": true}); pick != 0 {
		t.Fatalf("warmth decided a comparison it could not make: pick %d", pick)
	}
}

// Nothing warm means nothing to do, and a single candidate is not a choice.
func TestWarmthIsANoOpWithoutCandidatesOrEvidence(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	one := []instance{{Provider: config.Provider{ID: "acct-a"}, upstreamModel: "cold"}}
	if pick := gw.preferWarm(one, map[string]bool{"acct-a\x00cold": true}); pick != 0 {
		t.Fatalf("a single candidate was reordered: %d", pick)
	}
	two := []instance{
		{Provider: config.Provider{ID: "acct-a"}, upstreamModel: "cold"},
		{Provider: config.Provider{ID: "acct-a"}, upstreamModel: "warm"},
	}
	if pick := gw.preferWarm(two, nil); pick != 0 {
		t.Fatalf("warmth fired with no evidence: %d", pick)
	}
}

// The wiring matters as much as the helper. A warm tie must actually change
// which provider receives the request end to end, or preferWarm is dead code
// with good tests.
func TestWarmDeploymentActuallyReceivesTheRequest(t *testing.T) {
	same := 2.0
	coldHits, warmHits := 0, 0
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			coldHits++
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "cold"}}}})
		},
		func(w http.ResponseWriter, r *http.Request) {
			warmHits++
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "warm"}}}})
		},
	)
	// Warm evidence already exists for acct-b, and it is priced the same.
	gw.Usage = usage.Open("")
	gw.Usage.Add(usage.Event{Time: time.Now(), AccountID: "acct-b", Model: "gpt-4o-mini", CacheRead: 5000})
	gw.models = []catalog.Model{
		{ID: "gpt-4o-mini", AccountID: "acct-a", Price: catalog.Price{Input: &same, Output: &same, Currency: "USD", Source: "openrouter", Verified: true}},
		{ID: "gpt-4o-mini", AccountID: "acct-b", Price: catalog.Price{Input: &same, Output: &same, Currency: "USD", Source: "openrouter", Verified: true}},
	}
	gw.cfg.AutomaticRoutes.Enabled = true

	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"pea/economy","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if warmHits == 0 {
		t.Fatalf("the warm deployment was never used (cold=%d warm=%d)", coldHits, warmHits)
	}
}

// Warmth compared input rates only, so a deployment that was cheap to prompt
// with but expensive to read from beat the one that actually costs less. That is
// the same trap as ranking economy on input alone, in a place where it looks
// like a saving: the cache read is discounted, the long answer is not.
func TestWarmthNeverBeatsACheaperOutput(t *testing.T) {
	warmIn, warmOut := 1.0, 60.0
	coldIn, coldOut := 20.0, 2.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.models = []catalog.Model{
		{ID: "cold", AccountID: "acct-a", Price: catalog.Price{Input: &coldIn, Output: &coldOut, Currency: "USD", Source: "openrouter", Verified: true}},
		{ID: "warm", AccountID: "acct-a", Price: catalog.Price{Input: &warmIn, Output: &warmOut, Currency: "USD", Source: "openrouter", Verified: true}},
	}
	ranked := []instance{
		{Provider: config.Provider{ID: "acct-a"}, upstreamModel: "cold"},
		{Provider: config.Provider{ID: "acct-a"}, upstreamModel: "warm"},
	}

	if pick := gw.preferWarm(ranked, map[string]bool{"acct-a\x00warm": true}); pick != 0 {
		t.Fatalf("warmth overrode the cheaper deployment: pick %d", pick)
	}
}
