package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
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
	gw.cfg.Routes = map[string]string{"code": "llama3.2"}
	resp, account, err := gw.Chat(context.Background(), []byte(`{"temperature":0,"model":"code","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" || account != "acct-a" {
		t.Fatalf("resp %q account %s", resp.Content, account)
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
	gw.cfg.Routes = map[string]string{"code": "missing-model"}
	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"code","messages":[{"role":"user","content":"hi"}]}`))
	if err == nil || !strings.Contains(err.Error(), "not in the live catalog") {
		t.Fatalf("err %v", err)
	}
	if hits != 0 {
		t.Fatalf("upstream hits %d", hits)
	}
}
