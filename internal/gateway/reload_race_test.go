package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

// The config watcher replaces g.cfg while requests are in flight. Request
// paths must read it under the lock (run with -race).
func TestRequestsDoNotRaceAConfigReload(t *testing.T) {
	gw := pinnedGateway(t, 0, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]string{{"id": "m1"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "c", "model": "m1", "choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "x"}, "finish_reason": "stop"}}})
	})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			disk := gw.Config()
			disk.RequestEngine.PromptCache = []string{"auto", "off"}[i%2]
			disk.Optimization.SpendCeilingUSD = float64(i % 3)
			gw.saveMu.Lock()
			_, _ = gw.adopt(disk)
			gw.saveMu.Unlock()
		}
	}()
	for i := 0; i < 50; i++ {
		_, _, _ = gw.Chat(context.Background(), []byte(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`))
	}
	close(stop)
	wg.Wait()
}
