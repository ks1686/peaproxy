package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
)

func TestV2FixtureChatAndEngineStatus(t *testing.T) {
	s, _ := testServer(t)
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"llama3.2","messages":[{"role":"user","content":"hi"}]}`))
	chat.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, chat)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "hello from llama3.2") {
		t.Fatalf("chat status %d body %s", rr.Code, rr.Body.String())
	}
	engine := httptest.NewRequest(http.MethodGet, "/admin/engine", nil)
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, engine)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "apiKey") {
		t.Fatalf("engine status %d body %s", rr.Code, rr.Body.String())
	}
}

func TestV2CacheHitSkipsUpstreamUsage(t *testing.T) {
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		hits++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
			"usage":   map[string]int{"prompt_tokens": 9, "completion_tokens": 2},
		})
	}))
	t.Cleanup(up.Close)
	dir := t.TempDir()
	cfg := config.Config{
		SchemaVersion: 1,
		Providers: []config.Provider{{
			ID: "only", Adapter: "openai_compat", Tier: "paid", BaseURL: up.URL + "/v1",
		}},
	}
	cfg.RequestEngine.CacheResponses = true
	gw, err := gateway.New(cfg, filepath.Join(dir, "peaproxy.yaml"), adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	root := filepath.Join(dir, "home")
	s := New(Options{Gateway: gw, ClientRoot: root})
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("chat %d status %d %s", i, rr.Code, rr.Body.String())
		}
	}
	if hits != 1 {
		t.Fatalf("upstream hits = %d", hits)
	}
	recent := gw.Usage.Recent()
	if len(recent) != 2 || !recent[0].CacheHit || recent[0].PromptTokens != 0 || recent[1].PromptTokens != 9 {
		t.Fatalf("%#v", recent)
	}
	connect := httptest.NewRequest(http.MethodPost, "/admin/clients/opencode/connect", strings.NewReader(`{"model":"m"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, connect)
	if rr.Code != http.StatusOK {
		t.Fatalf("connect %d %s", rr.Code, rr.Body.String())
	}
	written, err := os.ReadFile(filepath.Join(root, "opencode.json"))
	if err != nil || !strings.Contains(string(written), "peaproxy") {
		t.Fatalf("connect file %v %s", err, written)
	}
	disconnect := httptest.NewRequest(http.MethodPost, "/admin/clients/opencode/disconnect", strings.NewReader(`{}`))
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, disconnect)
	if rr.Code != http.StatusOK {
		t.Fatalf("disconnect %d %s", rr.Code, rr.Body.String())
	}
	after, err := os.ReadFile(filepath.Join(root, "opencode.json"))
	if err != nil || strings.Contains(string(after), `"peaproxy"`) {
		t.Fatalf("disconnect file %v %s", err, after)
	}
}
