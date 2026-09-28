package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
)

func TestCancellationSoakBounded(t *testing.T) {
	dur := time.Second
	if testing.Short() {
		dur = 200 * time.Millisecond
	}
	if v := os.Getenv("PEAPROXY_SOAK"); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			t.Fatal(err)
		}
		dur = parsed
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
			http.Error(w, "still waiting", http.StatusGatewayTimeout)
		}
	}))
	t.Cleanup(up.Close)
	cfg := config.Config{
		SchemaVersion: 1,
		Providers: []config.Provider{{
			ID: "only", Adapter: "openai_compat", Tier: "paid", BaseURL: up.URL + "/v1",
		}},
	}
	gw, err := New(cfg, filepath.Join(t.TempDir(), "peaproxy.yaml"), adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	runtime.GC()
	base := runtime.NumGoroutine()
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _, _ = gw.Chat(ctx, body)
		}()
		cancel()
		<-done
	}
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	if got := runtime.NumGoroutine(); got > base+20 {
		t.Fatalf("goroutines grew from %d to %d", base, got)
	}
}
