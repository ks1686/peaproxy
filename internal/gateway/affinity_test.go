package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/router"
)

func TestSessionAffinityKeepsOneAccountThenRebindsAfterCooldown(t *testing.T) {
	hitsA, hitsB := 0, 0
	gw := policyGateway(t, "round-robin",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hitsA++
			if hitsA == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"quota"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "from-a"}}},
			})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hitsB++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "from-b"}}},
			})
		},
	)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"same chat"}]}`)
	resp, account, err := gw.Chat(context.Background(), body)
	if err != nil || resp.Content != "from-b" || account != "acct-b" {
		t.Fatalf("first %v content %q account %s", err, resp.Content, account)
	}
	resp, account, err = gw.Chat(context.Background(), body)
	if err != nil || account != "acct-b" || resp.Content != "from-b" {
		t.Fatalf("second %v content %q account %s", err, resp.Content, account)
	}
	if hitsA != 1 {
		t.Fatalf("cooled account was tried again, hitsA=%d hitsB=%d", hitsA, hitsB)
	}
}

func TestSessionAffinitySticksAcrossRoundRobinUntilTTL(t *testing.T) {
	hitsA, hitsB := 0, 0
	gw := policyGateway(t, "round-robin", countOK(&hitsA, "from-a"), countOK(&hitsB, "from-b"))
	gw.cfg.Failover.SessionAffinityTTL = "30ms"
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello pea"}]}`)
	if _, account, err := gw.Chat(context.Background(), body); err != nil || account != "acct-a" {
		t.Fatalf("first account %s err %v", account, err)
	}
	if _, account, err := gw.Chat(context.Background(), body); err != nil || account != "acct-a" {
		t.Fatalf("pinned account %s err %v", account, err)
	}
	if hitsB != 0 {
		t.Fatalf("round-robin moved a live session, hitsB=%d", hitsB)
	}
	time.Sleep(40 * time.Millisecond)
	if _, account, err := gw.Chat(context.Background(), body); err != nil || account != "acct-b" {
		t.Fatalf("after ttl account %s err %v hitsA=%d hitsB=%d", account, err, hitsA, hitsB)
	}
}

func TestSessionAffinityCanBeDisabled(t *testing.T) {
	hitsA, hitsB := 0, 0
	gw := policyGateway(t, "round-robin", countOK(&hitsA, "from-a"), countOK(&hitsB, "from-b"))
	off := false
	gw.cfg.Failover.SessionAffinity = &off
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello pea"}]}`)
	for i := 0; i < 2; i++ {
		if _, _, err := gw.Chat(context.Background(), body); err != nil {
			t.Fatal(err)
		}
	}
	if hitsA != 1 || hitsB != 1 {
		t.Fatalf("disabled affinity should rotate a=%d b=%d", hitsA, hitsB)
	}
}

func TestHeaderSessionReachesTheGateway(t *testing.T) {
	hitsA, hitsB := 0, 0
	gw := policyGateway(t, "round-robin", countOK(&hitsA, "from-a"), countOK(&hitsB, "from-b"))
	ctx := router.WithSession(context.Background(), "sess-1")
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"one"}]}`)
	other := []byte(`{"model":"m","messages":[{"role":"user","content":"two"}]}`)
	if _, account, err := gw.Chat(ctx, body); err != nil || account != "acct-a" {
		t.Fatalf("first %s %v", account, err)
	}
	if _, account, err := gw.Chat(ctx, other); err != nil || account != "acct-a" {
		t.Fatalf("header session should ignore a different body, account %s err %v", account, err)
	}
}
