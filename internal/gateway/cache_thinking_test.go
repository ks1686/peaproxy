package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

// prepare strips "-thinking-N" from the model before the body is hashed, so the
// budget lived only beside the body. A thinking request must not be served a
// cached non-thinking answer (or the reverse).
func TestResponseCacheSeparatesThinkingBudgets(t *testing.T) {
	var calls atomic.Int32
	gw := pinnedGateway(t, 0, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]string{{"id": "m1"}}})
			return
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "c", "model": "m1", "choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "x"}, "finish_reason": "stop"}}})
	})
	cfg := gw.Config()
	cfg.RequestEngine.CacheResponses = true
	gw.SetConfig(cfg)
	for _, model := range []string{"m1", "m1-thinking-1000"} {
		if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"`+model+`","temperature":0,"messages":[{"role":"user","content":"same"}]}`)); err != nil {
			t.Fatalf("%s: %v", model, err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2: the thinking request was answered from the non-thinking cache entry", got)
	}
}
