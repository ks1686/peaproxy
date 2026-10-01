package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
)

// #74: maxInFlight was only acquired on non-streaming Chat. Streams, Claude,
// Responses, images and embeddings never took a slot, so the limit described a
// fraction of the traffic and nothing else.

func oneModelListing() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
		}
	}
}

// pinnedGateway is one account with maxInFlight set.
func pinnedGateway(t *testing.T, max int, h http.HandlerFunc) *Gateway {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Providers:     []config.Provider{{ID: "acct-a", Adapter: "openai_compat", Tier: "paid", BaseURL: srv.URL + "/v1"}},
		RequestEngine: config.RequestEnginePrefs{MaxInFlight: max},
		Failover:      config.FailoverPrefs{},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	return gw
}

// concurrentCounter records the most requests it had open at once.
func concurrentCounter(h http.HandlerFunc) (*int, http.HandlerFunc) {
	var mu sync.Mutex
	var live int
	peakOut := new(int)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		live++
		if live > *peakOut {
			*peakOut = live
		}
		mu.Unlock()
		time.Sleep(120 * time.Millisecond)
		h(w, r)
		mu.Lock()
		live--
		mu.Unlock()
	})
	return peakOut, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			oneModelListing()(w, r)
			return
		}
		inner.ServeHTTP(w, r)
	}
}

func sseChunk(content string) string {
	return "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"" + content + "\"}}]}\n\n" +
		"data: [DONE]\n\n"
}

// A stream holds a slot for as long as it is streaming, which is the whole
// point: a stream is the most expensive thing on the wire.
func TestStreamsTakeAnAdmissionSlot(t *testing.T) {
	peak, h := concurrentCounter(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseChunk("hi")))
	})
	gw := pinnedGateway(t, 1, h)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var sb strings.Builder
			_, _ = gw.ChatStream(context.Background(), []byte(`{"model":"m"}`), &sb)
		}()
	}
	wg.Wait()

	if *peak > 1 {
		t.Errorf("%d streams ran at once with maxInFlight=1", *peak)
	}
}

// The same for a non-streaming call that already worked, kept so a future
// change to the stream path cannot be made by breaking the plain one.
func TestNonStreamingTakesAnAdmissionSlot(t *testing.T) {
	peak, h := concurrentCounter(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	})
	gw := pinnedGateway(t, 1, h)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = gw.Chat(context.Background(), []byte(`{"model":"m"}`))
		}()
	}
	wg.Wait()

	if *peak > 1 {
		t.Errorf("%d chat calls ran at once with maxInFlight=1", *peak)
	}
}

// An account that is merely busy is not a broken account. The request has to
// move to the next candidate instead of failing.
func TestAdmissionRejectionTriesTheNextAccount(t *testing.T) {
	var hitsB int
	gw := policyGateway(t, "fill-first",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				oneModelListing()(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "a"}}},
			})
		}),
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				oneModelListing()(w, r)
				return
			}
			hitsB++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "b"}}},
			})
		},
	)
	gw.admission.MaxInFlight = 1
	gw.admission.MaxQueue = 1
	gw.admission.Wait = 20 * time.Millisecond

	// Pin acct-a's only slot, so admitting it can only be rejected.
	release, err := gw.admission.Acquire(context.Background(), "acct-a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m"}`)); err != nil {
		t.Fatalf("a busy first account failed the whole request instead of trying the next: %v", err)
	}
	if hitsB == 0 {
		t.Error("the request did not reach the second account")
	}
}

// Being skipped for capacity is not evidence the account is unhealthy, so it
// must not cool down -- otherwise one burst takes an account out of rotation.
func TestAdmissionRejectionDoesNotCoolDownAnAccount(t *testing.T) {
	gw := policyGateway(t, "fill-first",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				oneModelListing()(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "a"}}},
			})
		}),
		countOK(new(int), "b"),
	)
	gw.admission.MaxInFlight = 1
	// The gate publishes its own limit when a request arrives, so pin it here
	// before taking the slot or the pin itself would be a no-op.
	gw.admission.MaxInFlight = 1
	gw.admission.MaxQueue = 1
	gw.admission.Wait = 20 * time.Millisecond

	release, err := gw.admission.Acquire(context.Background(), "acct-a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m"}`)); err != nil {
		t.Fatal(err)
	}
	for _, cd := range gw.Cooldowns() {
		if cd.AccountID == "acct-a" {
			t.Errorf("a capacity skip cooled the account down: %+v", cd)
		}
	}
}

// Every account busy is a different answer from every account broken: the
// client gets the admission error, not a lie about a cooldown.
func TestEveryAccountBusyReportsAdmissionNotCooldown(t *testing.T) {
	var hits int
	gw := pinnedGateway(t, 1, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			oneModelListing()(w, r)
			return
		}
		hits++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "a"}}},
		})
	})
	// The gate publishes its own limit when a request arrives, so pin it here
	// before taking the slot or the pin itself would be a no-op.
	gw.admission.MaxInFlight = 1
	gw.admission.MaxQueue = 1
	gw.admission.Wait = 20 * time.Millisecond

	release, err := gw.admission.Acquire(context.Background(), "acct-a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, _, err = gw.Chat(context.Background(), []byte(`{"model":"m"}`))
	if err == nil {
		t.Fatal("expected an error when the only account is full")
	}
	if got := err.Error(); strings.Contains(got, "cooling down") {
		t.Errorf("reported a cooldown that was never applied: %v", err)
	}
	if hits != 0 {
		t.Errorf("the upstream was called %d times despite a full gate", hits)
	}
}

// maxInFlight is configuration, so it is read when the gateway is built or
// rebuilt -- not written onto a live gate on every request, which raced every
// reader inside Acquire.
func TestMaxInFlightIsPickedUpOnRebuild(t *testing.T) {
	gw := pinnedGateway(t, 0, countOK(new(int), "a"))
	if gw.admission.MaxInFlight != 0 {
		t.Errorf("maxInFlight=0 was published to the gate as %d", gw.admission.MaxInFlight)
	}
	cfg := gw.Config()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "acct-b", Adapter: "openai_compat", BaseURL: "http://127.0.0.1:1/v1"})
	cfg.RequestEngine.MaxInFlight = 3
	gw.SetConfig(cfg)
	if gw.admission.MaxInFlight != 3 {
		t.Errorf("a rebuild did not publish the new limit: %d", gw.admission.MaxInFlight)
	}
}

// #80: a config handed out for marshalling must not share the gateway's
// backing array with RemoveProvider's in-place filter.
func TestConfigDoesNotShareProviderBackingArray(t *testing.T) {
	gw := policyGateway(t, "fill-first", countOK(new(int), "a"), countOK(new(int), "b"))
	before := gw.Config()
	if len(before.Providers) != 2 {
		t.Fatalf("got %d providers, want 2", len(before.Providers))
	}
	if err := gw.RemoveProvider(context.Background(), "acct-a"); err != nil {
		t.Fatal(err)
	}
	after := gw.Config()
	if len(after.Providers) != 1 || after.Providers[0].ID != "acct-b" {
		t.Errorf("after removal: %+v", after.Providers)
	}
	// The copy taken before the removal must still describe what it described,
	// or a caller marshalling it concurrently writes the wrong config.
	if len(before.Providers) != 2 || before.Providers[0].ID != "acct-a" {
		t.Errorf("an earlier Config() call was mutated underneath its holder: %+v", before.Providers)
	}
}
