package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
)

// #67: image, edit and embedding requests did not use the v2 attempt budget or
// the request deadline, and a 504 was treated as transient and sent to the
// next account.
//
// A 504 is the worst possible answer to "did the provider generate this image?"
// -- a gateway in front of the provider returns 504 precisely when it stopped
// waiting, which is *after* the provider may well have generated and billed an
// image. Replaying it against a second account buys a duplicate bill and
// returns whichever finished first.

func imageGateway(t *testing.T, deadline string, handlers ...http.HandlerFunc) *Gateway {
	t.Helper()
	if len(handlers) < 2 {
		t.Fatal("need at least two handlers")
	}
	ids := []string{"acct-a", "acct-b"}
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
		Providers:     providers,
		Failover:      config.FailoverPrefs{Policy: "fill-first"},
		RequestEngine: config.RequestEnginePrefs{Deadline: deadline},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	// Tag the models so they are offered for image out.
	for id := range gw.models {
		gw.models[id].Modalities = append(gw.models[id].Modalities, "image_out")
	}
	return gw
}

func imageListing() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
		}
	}
}

func okImage(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": "aW1n"}}})
}

// A completed 504 cannot prove the provider did not generate an image, so it
// must not be replayed against another account.
func TestImage504IsNotReplayedOnAnotherAccount(t *testing.T) {
	var hitsB int
	gw := imageGateway(t, "",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"error":"upstream timed out"}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			hitsB++
			okImage(w, r)
		},
	)

	if _, _, err := gw.GenerateImage(context.Background(), []byte(`{"model":"m"}`)); err == nil {
		t.Fatal("expected the 504 to surface")
	}
	if hitsB != 0 {
		t.Errorf("a 504 replayed the image request %d time(s) on another account", hitsB)
	}
}

// 502 is the same argument: a proxy in front gave up, which says nothing about
// what the provider behind it did.
func TestImage502IsNotReplayedOnAnotherAccount(t *testing.T) {
	var hitsB int
	gw := imageGateway(t, "",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			w.WriteHeader(http.StatusBadGateway)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			hitsB++
			okImage(w, r)
		},
	)

	if _, _, err := gw.GenerateImage(context.Background(), []byte(`{"model":"m"}`)); err == nil {
		t.Fatal("expected the 502 to surface")
	}
	if hitsB != 0 {
		t.Errorf("a 502 replayed the image request %d time(s) on another account", hitsB)
	}
}

// A plain 500 is the same story, and was already in scope before #67; pinning
// it so the rule is stated once rather than per status code.
func TestImage500IsNotReplayedOnAnotherAccount(t *testing.T) {
	var hitsB int
	gw := imageGateway(t, "",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			hitsB++
			okImage(w, r)
		},
	)

	if _, _, err := gw.GenerateImage(context.Background(), []byte(`{"model":"m"}`)); err == nil {
		t.Fatal("expected the 500 to surface")
	}
	if hitsB != 0 {
		t.Errorf("a 500 replayed the image request %d time(s) on another account", hitsB)
	}
}

// A 429 is the opposite: the provider is telling us it refused, so nothing was
// generated and moving to the next account is free.
func TestImage429StillMovesToTheNextAccount(t *testing.T) {
	var hitsB int
	gw := imageGateway(t, "",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			w.WriteHeader(http.StatusTooManyRequests)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			hitsB++
			okImage(w, r)
		},
	)

	if _, _, err := gw.GenerateImage(context.Background(), []byte(`{"model":"m"}`)); err != nil {
		t.Fatalf("a refused request failed instead of trying the next account: %v", err)
	}
	if hitsB == 0 {
		t.Error("a 429 did not move to the next account")
	}
}

// An image call took no deadline at all, so a hung provider held the request
// open indefinitely.
func TestImageHonoursTheRequestDeadline(t *testing.T) {
	gw := imageGateway(t, "150ms",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			time.Sleep(3 * time.Second)
			okImage(w, r)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			okImage(w, r)
		},
	)

	start := time.Now()
	_, _, err := gw.GenerateImage(context.Background(), []byte(`{"model":"m"}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a deadline error")
	}
	if elapsed > time.Second {
		t.Errorf("the request ran for %v with a 150ms deadline", elapsed)
	}
}

// Embeddings are idempotent, so they may fail over -- but they were still
// outside the deadline and the attempt budget.
func TestEmbeddingsHonourTheRequestDeadline(t *testing.T) {
	gw := imageGateway(t, "150ms",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			time.Sleep(3 * time.Second)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		},
	)
	for id := range gw.models {
		gw.models[id].Modalities = append(gw.models[id].Modalities, "embeddings")
	}

	start := time.Now()
	_, _, err := gw.CreateEmbeddings(context.Background(), []byte(`{"model":"m","input":"hi"}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a deadline error")
	}
	if elapsed > time.Second {
		t.Errorf("embeddings ran for %v with a 150ms deadline", elapsed)
	}
}

// The attempt budget bounds total upstream calls across accounts. An image call
// that ignores it can fan out to every configured account no matter what
// maxAttempts says.
func TestImageRespectsTheAttemptBudget(t *testing.T) {
	var hits int
	mk := func() http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				imageListing()(w, r)
				return
			}
			hits++
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}
	srvA := httptest.NewServer(mk())
	defer srvA.Close()
	srvB := httptest.NewServer(mk())
	defer srvB.Close()
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Failover:      config.FailoverPrefs{Policy: "fill-first"},
		Providers: []config.Provider{
			{ID: "acct-a", Adapter: "openai_compat", Tier: "paid", BaseURL: srvA.URL + "/v1"},
			{ID: "acct-b", Adapter: "openai_compat", Tier: "paid", BaseURL: srvB.URL + "/v1"},
		},
		RequestEngine: config.RequestEnginePrefs{MaxAttempts: 1},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	for id := range gw.models {
		gw.models[id].Modalities = append(gw.models[id].Modalities, "image_out")
	}

	if _, _, err := gw.GenerateImage(context.Background(), []byte(`{"model":"m"}`)); err == nil {
		t.Fatal("expected an error")
	}
	if hits > 1 {
		t.Errorf("maxAttempts=1 but the image path made %d upstream calls", hits)
	}
}
