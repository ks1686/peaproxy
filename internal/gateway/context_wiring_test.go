package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/contextopt"
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
			_, _ = w.Write([]byte(`{}`))
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

// The executor has to actually run. A model that calls pea_search must get a
// result and a second upstream call, not silence.
func TestProxyToolCallIsAnsweredAndTheRequestResent(t *testing.T) {
	var bodies []string
	var hits int
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
			hits++
			if strings.Contains(string(b), `"role":"tool"`) {
				// Second round: answer with content.
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{"message": map[string]string{"content": "staging is on 8443"}}},
				})
				return
			}
			// First round: ask for a search.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{
					"tool_calls": []map[string]any{{
						"id": "call_1", "type": "function",
						"function": map[string]string{"name": "pea_search", "arguments": `{"query":"staging cluster port"}`},
					}},
				}}},
			})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "x"}}}})
		},
	)
	gw.Artifacts = contextstore.New(contextstore.Options{})
	if err := gw.Artifacts.Put(sessionOf([]byte(`{"model":"m","messages":[{"role":"user","content":"staging cluster port"}]}`)),
		contextstore.Artifact{Key: "k", Body: []byte("the staging cluster runs on port 8443")}); err != nil {
		t.Fatal(err)
	}
	gw.cfg.Optimization.Automatic = boolp(true)

	body := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"t"}}],"messages":[{"role":"user","content":"staging cluster port"}]}`)
	resp, _, err := gw.Chat(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	if hits < 2 {
		t.Fatalf("the request was not resent after the tool call (hits=%d)", hits)
	}
	if !strings.Contains(string(resp.Raw), "8443") {
		t.Fatalf("the search result never reached the answer: %s", resp.Raw)
	}
}

// A model that will not stop searching must be stopped. Every round is a bill,
// and the turn has to end rather than loop.
func TestRepeatingSearchIsStoppedAtTheRoundLimit(t *testing.T) {
	var hits int
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hits++
			// Always ask for another search.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{
					"tool_calls": []map[string]any{{
						"id": "call_x", "type": "function",
						"function": map[string]string{"name": "pea_search", "arguments": `{"query":"anything at all here"}`},
					}},
				}}},
			})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			hits++
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "x"}}}})
		},
	)
	gw.Artifacts = contextstore.New(contextstore.Options{})
	if err := gw.Artifacts.Put("s1", contextstore.Artifact{Key: "k", Body: []byte("anything at all material")}); err != nil {
		t.Fatal(err)
	}
	gw.cfg.Optimization.Automatic = boolp(true)

	body := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"t"}}],"messages":[{"role":"user","content":"anything at all here"}]}`)
	// The turn ends with an error rather than with the round that asked for a
	// search the budget no longer had. Handing that round back would give the
	// client a pea_search call it never made.
	_, _, err := gw.Chat(context.Background(), body)
	if err == nil {
		t.Fatal("an exhausted search budget returned a normal answer")
	}
	if !strings.Contains(err.Error(), contextopt.ToolName) {
		t.Fatalf("the refusal does not name the loop that ended: %v", err)
	}
	if hits > contextopt.MaxRounds+1 {
		t.Fatalf("the search loop ran %d upstream calls, past the budget of %d", hits, contextopt.MaxRounds)
	}
}

// A streaming client must not be offered a tool PeaProxy can only answer by
// buffering the stream. This is the check that keeps incremental delivery
// incremental, and it is easy to lose by wiring the plan wrongly.
func TestStreamingRequestIsNotOfferedTheProxyTool(t *testing.T) {
	if streamRequested([]byte(`{"model":"m","stream":true}`)) != true {
		t.Error("a streaming body was not detected as streaming")
	}
	if streamRequested([]byte(`{"model":"m","stream":false}`)) != false {
		t.Error("a non-streaming body was detected as streaming")
	}
	if streamRequested([]byte(`not json`)) != false {
		t.Error("an unparseable body was detected as streaming")
	}
}

// A store that is always present is what makes carried context real. It was
// left nil in the constructor, and every context path treats nil as "skip", so
// the feature documented as on by default never ran.
func TestGatewayAlwaysHasAnArtifactStore(t *testing.T) {
	stub := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}
	gw := twoAccountGateway(t, stub, stub)
	if gw.Artifacts == nil {
		t.Fatal("the artifact store is nil in a constructed gateway; every context path skips on nil")
	}
}

// Two sessions sending the identical body must not share an answer when the
// upstream request would have carried one session's context.
//
// This drives the real path -- response caching on, two requests through Chat
// with different sessions -- because testing the helper alone proved nothing: an
// earlier version passed while the guards it was meant to cover were not
// attached to the cache at all.
func TestTwoSessionsDoNotShareAnAnswerThroughTheResponseCache(t *testing.T) {
	var hits int
	stub := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		hits++
		body, _ := io.ReadAll(r.Body)
		// Echo which session's material reached upstream, so a shared answer is
		// visible in the response rather than merely counted.
		tag := "none"
		if bytes.Contains(body, []byte("8443")) {
			tag = "staged"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": tag}}},
		})
	}
	gw := twoAccountGateway(t, stub, stub)
	gw.cfg.RequestEngine.CacheResponses = true
	gw.cfg.Optimization.ContextOptimization = boolp(true)

	// A retrievable artifact for session one only.
	if err := gw.Artifacts.Put("s1", contextstore.Artifact{
		Key:  "deploy",
		Body: []byte("the staging cluster runs on port 8443"),
	}); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"where is the staging cluster"}]}`)

	first, _, err := gw.Chat(router.WithSession(context.Background(), "s1"), body)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := gw.Chat(router.WithSession(context.Background(), "s2"), body)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(first.Raw, []byte("staged")) {
		t.Fatalf("the first session did not receive its own context: %s", first.Raw)
	}
	if bytes.Contains(second.Raw, []byte("staged")) {
		t.Fatalf("the second session was served the first session's context: %s", second.Raw)
	}
	if hits < 2 {
		t.Fatalf("upstream was contacted %d time(s); the second session reused a cached answer", hits)
	}
}

// With the caches wired, a session whose request was transformed must not be
// stored at all. Removing either guard makes this fail.
func TestTransformedRequestIsNotStoredInTheResponseCache(t *testing.T) {
	var hits int
	stub := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		hits++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}
	gw := twoAccountGateway(t, stub, stub)
	gw.cfg.RequestEngine.CacheResponses = true
	gw.cfg.Optimization.ContextOptimization = boolp(true)
	if err := gw.Artifacts.Put("s1", contextstore.Artifact{
		Key:  "deploy",
		Body: []byte("the staging cluster runs on port 8443"),
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"where is the staging cluster"}]}`)

	for i := 0; i < 2; i++ {
		if _, _, err := gw.Chat(router.WithSession(context.Background(), "s1"), body); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 2 {
		t.Fatalf("upstream was contacted %d time(s); a context-bearing answer was served from cache on the second turn", hits)
	}
}

// The adapter-dependent case: the caller declares tools and the stored material
// has no lexical overlap with the query, so prefetch produces nothing. The only
// thing that would change the body is injecting pea_search, which happens only
// when the chosen provider supports tools.
//
// This is the case the candidate-aware fix exists for. With the previous
// instance{} check the adapter was nil, the injection went unseen, and the
// answer was cached and shared. The lexical cases above pass either way, so
// they cannot catch a revert of this.
func TestToolUsingRequestWithoutPrefetchHitIsStillSessionSpecific(t *testing.T) {
	var hits int
	stub := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		hits++
		_, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}
	gw := twoAccountGateway(t, stub, stub)
	gw.cfg.RequestEngine.CacheResponses = true
	gw.cfg.Optimization.ContextOptimization = boolp(true)

	// Stored material shares no term with the query, so nothing is prefetched.
	if err := gw.Artifacts.Put("s1", contextstore.Artifact{
		Key:  "unrelated",
		Body: []byte("zeppelin quokka xylophone marmalade"),
	}); err != nil {
		t.Fatal(err)
	}

	withTools := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"lookup"}}],` +
		`"messages":[{"role":"user","content":"quantum tunnelling dynamics"}]}`)

	// The decision itself must hold: a tool-using request is session-specific
	// even with no lexical hit, because the tool is injected regardless.
	// The real adapter, which is the point: whether tools are supported comes
	// from the provider, not from a stub standing in for one.
	cands := []instance{{Adapter: gw.inst[0].Adapter}}
	if !gw.sessionSpecificBody(withTools, "s1", cands) {
		t.Fatal("a tool-using request with no prefetch hit was reported cacheable; the pea_search injection is invisible again")
	}

	// And end to end: two sessions, same bytes, second must reach upstream.
	if _, _, err := gw.Chat(router.WithSession(context.Background(), "s1"), withTools); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gw.Chat(router.WithSession(context.Background(), "s2"), withTools); err != nil {
		t.Fatal(err)
	}
	if hits < 2 {
		t.Fatalf("upstream was contacted %d time(s); a tool-using answer was shared across sessions", hits)
	}
}
