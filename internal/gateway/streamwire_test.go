package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ks1686/peaproxy/internal/config"
)

// sseLines splits an SSE body the way a client would.
func sseLines(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m); err != nil {
			t.Fatalf("bad SSE line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// #79: a translated stream wrote past the route rewriter, so the client saw the
// upstream model id instead of the one it asked for. A route alias is the
// everyday case: the client asks for "fast", the upstream answers "big-model".
func TestTranslatedResponsesStreamReturnsTheClientModelID(t *testing.T) {
	upstream := fakeOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		// The upstream names itself here; the client asked for "client-model".
		_, _ = w.Write([]byte("data: {\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	})

	g := streamGateway(t, upstream.URL)
	out := &strings.Builder{}
	if _, err := g.ResponsesStream(context.Background(), responsesBody("client-model"), out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "upstream-model") {
		t.Errorf("the client saw the upstream model id:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "client-model") {
		t.Errorf("the client's own model id is missing:\n%s", out.String())
	}
}

// Same for the Claude wire: message_start carries the model, and the
// translator copied the upstream id into it.
func TestTranslatedClaudeStreamReturnsTheClientModelID(t *testing.T) {
	upstream := fakeOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	})

	g := streamGateway(t, upstream.URL)
	out := &strings.Builder{}
	if _, err := g.ClaudeChatStream(context.Background(), claudeBody("client-model"), out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "upstream-model") {
		t.Errorf("the client saw the upstream model id:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "client-model") {
		t.Errorf("the client's own model id is missing:\n%s", out.String())
	}
}

// #78: only the non-streaming native path recorded the response id and pinned
// the next call to that account. Codex-style clients stream, so a follow-up with
// previous_response_id could land on an account that never saw the response.
//
// The assertion is on the id the client was actually handed, rather than on one
// this test hardcodes: the translator mints its own, so reading it back out of
// the stream is what makes this the real invariant.
func TestStreamedResponsesBindTheContinuation(t *testing.T) {
	upstream := fakeOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})

	g := streamGateway(t, upstream.URL)
	out := &strings.Builder{}
	account, err := g.ResponsesStream(context.Background(), responsesBody("m"), out)
	if err != nil {
		t.Fatal(err)
	}
	if account == "" {
		t.Fatal("no account reported")
	}
	emitted := responseIDFromStream(t, out.String())
	if emitted == "" {
		t.Fatalf("the stream carried no response id:\n%s", out.String())
	}
	if got := g.continuationAccount(emitted, "m"); got != account {
		t.Errorf("the streamed id %q bound to %q, want %q: a previous_response_id follow-up would not return to this account", emitted, got, account)
	}
}

// responseIDFromStream returns the id of the last response.completed event,
// which is what a client quotes back as previous_response_id.
func responseIDFromStream(t *testing.T, body string) string {
	t.Helper()
	id := ""
	for _, ev := range sseLines(t, body) {
		if ev["type"] != "response.completed" {
			continue
		}
		if resp, ok := ev["response"].(map[string]any); ok {
			if v, ok := resp["id"].(string); ok {
				id = v
			}
		}
	}
	return id
}

// A streamed call that names a previous response must be routed to the account
// that produced it.
func TestStreamedResponsesHonoursPreviousResponseID(t *testing.T) {
	var mu sync.Mutex
	var served []string
	hit := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
				return
			}
			flusher, _ := w.(http.Flusher)
			mu.Lock()
			served = append(served, name)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
	}
	g := twoAccountGateway(t, hit("a"), hit("b"))
	out := &strings.Builder{}
	if _, err := g.ResponsesStream(context.Background(), responsesBody("m"), out); err != nil {
		t.Fatal(err)
	}
	emitted := responseIDFromStream(t, out.String())
	if emitted == "" {
		t.Fatalf("the stream carried no response id:\n%s", out.String())
	}
	pinned := g.continuationAccount(emitted, "m")
	if pinned == "" {
		t.Fatalf("the streamed id %q was not bound to any account", emitted)
	}
	wantSecond := pinned == "acct-b"

	mu.Lock()
	served = nil
	mu.Unlock()
	body := []byte(strings.Replace(string(responsesBody("m")), `"input"`, `"previous_response_id":"`+emitted+`","input"`, 1))
	if _, err := g.ResponsesStream(context.Background(), body, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(served) != 1 {
		t.Fatalf("the follow-up tried %v, want exactly the pinned account", served)
	}
	if gotSecond := served[0] == "b"; gotSecond != wantSecond {
		t.Errorf("the follow-up went to %q; the response was produced by %q", served[0], pinned)
	}
}

func responsesBody(model string) []byte {
	return []byte(`{"model":"` + model + `","input":"hi","stream":true}`)
}

func claudeBody(model string) []byte {
	return []byte(`{"model":"` + model + `","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
}

// fakeOpenAI serves one SSE endpoint and records that it was hit.
func fakeOpenAI(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}, {"id": "upstream-model"}}})
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func streamGateway(t *testing.T, baseURL string) *Gateway {
	t.Helper()
	cfg := config.Default()
	// A route alias is the everyday case the issue describes: the client asks
	// for "client-model", the route resolves to the upstream's own id, and the
	// upstream echoes that id back in every event.
	cfg.Routes = map[string]string{"client-model": "upstream-model"}
	cfg.Providers = []config.Provider{{ID: "only", Adapter: "openai_compat", Tier: "paid", BaseURL: baseURL + "/v1"}}
	g, err := New(cfg, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	g.Refresh(context.Background())
	return g
}

var _ = io.Discard
