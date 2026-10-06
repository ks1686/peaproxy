package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/contextstore"
	"github.com/ks1686/peaproxy/internal/router"
)

// The wiring matters more than the helper. Prefetch is built and tested; this
// asserts a request that can benefit from stored context actually receives it,
// which is the difference between a feature and a package.
func TestStoredContextReachesTheUpstreamRequest(t *testing.T) {
	var seen string
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			b, _ := io.ReadAll(r.Body)
			seen = string(b)
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{}`))
		},
	)
	gw.Artifacts = contextstore.New(contextstore.Options{})
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"where is the staging cluster"}]}`)
	if err := gw.Artifacts.Put(sessionOf(body), contextstore.Artifact{
		Key:  "k",
		Body: []byte("the staging cluster runs on port 8443"),
	}); err != nil {
		t.Fatal(err)
	}
	gw.cfg.Optimization.Automatic = boolp(true)

	if _, _, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "port 8443") {
		t.Fatalf("stored context never reached the upstream request: %s", seen)
	}
}

// Off means off. The v3 default is opinionated, but a user who turned
// optimization off must get byte-identical requests, or the setting is a lie.
func TestContextOptimizationOffLeavesTheRequestAlone(t *testing.T) {
	var seen string
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			b, _ := io.ReadAll(r.Body)
			seen = string(b)
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
		},
	)
	gw.Artifacts = contextstore.New(contextstore.Options{})
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"where is the staging cluster"}]}`)
	if err := gw.Artifacts.Put(sessionOf(body), contextstore.Artifact{
		Key:  "k",
		Body: []byte("staging is on port 8443"),
	}); err != nil {
		t.Fatal(err)
	}
	gw.cfg.Optimization.Automatic = boolp(false)

	if _, _, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(seen, "port 8443") {
		t.Fatalf("context was injected while optimization was off: %s", seen)
	}
}

// With nothing stored there is nothing to add, and a proxy that pads every
// request with an empty context block is spending tokens for no reason.
func TestNoStoredContextMeansNoAddedBytes(t *testing.T) {
	var seen string
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			b, _ := io.ReadAll(r.Body)
			seen = string(b)
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
		},
	)
	gw.Artifacts = contextstore.New(contextstore.Options{})
	gw.cfg.Optimization.Automatic = boolp(true)

	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(seen, "peaproxy reference material") {
		t.Fatalf("an empty context block was injected: %s", seen)
	}
}

// The retrieval tool must not reach a request whose client never declared
// tools, whatever the caller does elsewhere. This is the leak the whole design
// turns on.
func TestProxyToolIsNotInjectedForAToollessClient(t *testing.T) {
	var seen string
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			b, _ := io.ReadAll(r.Body)
			seen = string(b)
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
		},
	)
	gw.Artifacts = contextstore.New(contextstore.Options{})
	gw.cfg.Optimization.Automatic = boolp(true)

	// No "tools" key in the client request.
	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(seen, "pea_search") {
		t.Fatalf("a proxy-owned tool reached a client that cannot run tools: %s", seen)
	}
}

// A client that does use tools gets the tool, because the contract can be kept.
func TestProxyToolIsInjectedForAToolUsingClient(t *testing.T) {
	var seen string
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			b, _ := io.ReadAll(r.Body)
			seen = string(b)
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
		},
	)
	gw.Artifacts = contextstore.New(contextstore.Options{})
	gw.cfg.Optimization.Automatic = boolp(true)

	body := `{"model":"m","tools":[{"type":"function","function":{"name":"caller_tool","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hi"}]}`
	if _, _, err := gw.Chat(context.Background(), []byte(body)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "pea_search") {
		t.Fatalf("a tool-using client did not receive the proxy tool: %s", seen)
	}
	if !strings.Contains(seen, "caller_tool") {
		t.Fatalf("the caller's tool was lost: %s", seen)
	}
}

// sessionOf is the id the router derives from a body. Context is keyed by it,
// so a fixture storing under any other name exercises nothing.
func sessionOf(body []byte) string { return router.SessionFromBody(body) }

var _ = router.RouteAuto
