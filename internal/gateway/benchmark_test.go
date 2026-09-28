package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
)

func BenchmarkChatPassThrough(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "model"}}})
		case "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model":   "model",
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			})
		}
	}))
	b.Cleanup(upstream.Close)
	cfg := config.Config{SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317, Providers: []config.Provider{{
		ID: "local", Adapter: "openai_compat", Tier: "local", BaseURL: upstream.URL + "/v1",
	}}}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		b.Fatal(err)
	}
	gw.Refresh(context.Background())

	for _, size := range []int{1 << 10, 100 << 10, 1 << 20} {
		body := []byte(`{"model":"model","messages":[{"role":"user","content":"` + strings.Repeat("x", size) + `"}]}`)
		b.Run("body_bytes="+strconv.Itoa(size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := gw.Chat(context.Background(), body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkChatConcurrency(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "model"}}})
		case "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model":   "model",
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			})
		}
	}))
	b.Cleanup(upstream.Close)
	cfg := config.Config{SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317, Providers: []config.Provider{{
		ID: "local", Adapter: "openai_compat", Tier: "local", BaseURL: upstream.URL + "/v1",
	}}}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		b.Fatal(err)
	}
	gw.Refresh(context.Background())
	body := []byte(`{"model":"model","messages":[{"role":"user","content":"` + strings.Repeat("x", 1024) + `"}]}`)
	for _, workers := range []int{1, 16, 64} {
		b.Run("workers="+strconv.Itoa(workers), func(b *testing.B) {
			b.ReportAllocs()
			var wg sync.WaitGroup
			errCh := make(chan error, 1)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				wg.Add(workers)
				for w := 0; w < workers; w++ {
					go func() {
						defer wg.Done()
						if _, _, err := gw.Chat(context.Background(), body); err != nil {
							select {
							case errCh <- err:
							default:
							}
						}
					}()
				}
				wg.Wait()
			}
			b.StopTimer()
			select {
			case err := <-errCh:
				b.Fatal(err)
			default:
			}
		})
	}
}
