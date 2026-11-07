package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/config"
)

func TestRouteNameRewritesUpstreamModelAndListsAlias(t *testing.T) {
	var gotBody []byte
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "llama3.2"}}})
				return
			}
			gotBody, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
			})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "other"}}})
				return
			}
			t.Errorf("second account should not see the route")
			w.WriteHeader(http.StatusBadGateway)
		},
	)
	gw.cfg.Routes = map[string]config.Route{"code": {Target: "llama3.2"}}
	resp, account, err := gw.Chat(context.Background(), []byte(`{"temperature":0,"model":"code","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" || account.Account != "acct-a" {
		t.Fatalf("resp %q account %s", resp.Content, account.Account)
	}
	if !strings.Contains(string(gotBody), `"model":"llama3.2"`) || strings.Contains(string(gotBody), `"model":"code"`) {
		t.Fatalf("upstream body %s", gotBody)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(gotBody)), `{"temperature":0,`) {
		t.Fatalf("key order changed: %s", gotBody)
	}
	var alias bool
	for _, m := range gw.Listed("all") {
		if m.ID == "code" && m.AliasOf == "llama3.2" {
			alias = true
		}
	}
	if !alias {
		t.Fatalf("listed %#v", gw.Listed("all"))
	}
}

func TestRouteNameMissingTargetDoesNotCallUpstream(t *testing.T) {
	hits := 0
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "llama3.2"}}})
				return
			}
			hits++
			w.WriteHeader(http.StatusOK)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "llama3.2"}}})
				return
			}
			hits++
			w.WriteHeader(http.StatusOK)
		},
	)
	gw.cfg.Routes = map[string]config.Route{"code": {Target: "missing-model"}}
	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"code","messages":[{"role":"user","content":"hi"}]}`))
	if err == nil || !strings.Contains(err.Error(), "not in the live catalog") {
		t.Fatalf("err %v", err)
	}
	if hits != 0 {
		t.Fatalf("upstream hits %d", hits)
	}
}

// A route pinned to one account is a provider choice, not a failover hint: the
// client named a provider, so the request must not silently move to another.
func TestPinnedRouteUsesOnlyTheNamedAccount(t *testing.T) {
	var mu sync.Mutex
	var served []string
	handler := func(id string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			mu.Lock()
			served = append(served, id)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
			})
		}
	}
	gw := twoAccountGateway(t, handler("acct-a"), handler("acct-b"))
	gw.cfg.Routes = map[string]config.Route{"pinned": {Target: "m", Account: "acct-b"}}

	resp, account, err := gw.Chat(context.Background(), []byte(`{"model":"pinned","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" || account.Account != "acct-b" {
		t.Fatalf("resp %q account %s, want acct-b", resp.Content, account.Account)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(served) != 1 || served[0] != "acct-b" {
		t.Fatalf("upstream hits %v, want only acct-b", served)
	}

	var row bool
	for _, m := range gw.Listed("all") {
		if m.ID != "pinned" {
			continue
		}
		row = true
		if m.AccountID != "acct-b" || m.AliasOf != "m" {
			t.Fatalf("route row = %#v, want acct-b -> m", m)
		}
	}
	if !row {
		t.Fatal("pinned route was not listed")
	}
}

// The other account staying hot is not a reason to use it: a cooled pinned
// account must surface as a cooldown, not as a failover.
func TestPinnedRouteCooledAccountDoesNotFailOver(t *testing.T) {
	hits := 0
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hits++
			w.WriteHeader(http.StatusOK)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hits++
			w.WriteHeader(http.StatusOK)
		},
	)
	gw.cfg.Routes = map[string]config.Route{"pinned": {Target: "m", Account: "acct-b"}}
	gw.cool["acct-b"] = cooldownSlots{wide: Cooldown{AccountID: "acct-b", Until: time.Now().Add(time.Minute)}}

	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"pinned","messages":[{"role":"user","content":"hi"}]}`))
	if err == nil {
		t.Fatal("the request was served although the pinned account is cooling down")
	}
	if !strings.Contains(err.Error(), "acct-b") {
		t.Fatalf("the cooldown does not name the pinned account: %v", err)
	}
	if strings.Contains(err.Error(), "acct-a") {
		t.Fatalf("the cooldown names an account the route does not call: %v", err)
	}
	if hits != 0 {
		t.Fatalf("upstream hits %d, want none", hits)
	}
}
