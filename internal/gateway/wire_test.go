package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRouteEchoesClientModel(t *testing.T) {
	var got []byte
	gw := policyGateway(t, "fill-first",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "llama3.2"}}})
				return
			}
			got, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model":   "llama3.2",
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
			})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "other"}}})
				return
			}
			t.Error("second account")
			w.WriteHeader(http.StatusBadGateway)
		},
	)
	gw.cfg.Routes = map[string]string{"code": "llama3.2"}
	resp, account, err := gw.Chat(context.Background(), []byte(`{"model":"code","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || account != "acct-a" || resp.Content != "ok" {
		t.Fatalf("%v %s %#v", err, account, resp)
	}
	if !strings.Contains(string(got), `"model":"llama3.2"`) {
		t.Fatalf("upstream %s", got)
	}
	if !strings.Contains(string(resp.Raw), `"model":"code"`) || strings.Contains(string(resp.Raw), "llama3.2") {
		t.Fatalf("client %s", resp.Raw)
	}
}

func TestTransientRetriesSameAccountOnce(t *testing.T) {
	hitsA, hitsB := 0, 0
	gw := policyGateway(t, "fill-first",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hitsA++
			if hitsA == 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
			})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hitsB++
			w.WriteHeader(http.StatusOK)
		},
	)
	resp, account, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || account != "acct-a" || resp.Content != "ok" || hitsA != 2 || hitsB != 0 {
		t.Fatalf("err %v account %s hits %d/%d content %q", err, account, hitsA, hitsB, resp.Content)
	}
}

func TestForbiddenDoesNotFailOver(t *testing.T) {
	hitsB := 0
	gw := policyGateway(t, "fill-first",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"no"}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hitsB++
			w.WriteHeader(http.StatusOK)
		},
	)
	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if err == nil || hitsB != 0 {
		t.Fatalf("err %v hitsB %d", err, hitsB)
	}
}
