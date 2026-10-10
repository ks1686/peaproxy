package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// #71: /v1/chat/completions streaming fails over on a retryable error, a
// transient 502/504, or a bad prelude. The Claude and Responses streams only
// failed over on a retryable error or a slow prelude, so a 502 was returned to
// the client even when another account could have taken the request -- and the
// stream guard had held the bytes back specifically so failover was possible.

func streamOnce(w http.ResponseWriter) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

// A streaming harness stops the turn when the first account answers 400.
// Copilot does that for a model that is not on chat/completions
// (unsupported_api_for_model) while another account can still serve it.
// The non-streaming path already moves on. The stream must too, and it
// must not cool the account down for a refusal that says nothing about quota.
func TestChatStreamFailsOverWhenTheModelRefusesTheProtocol(t *testing.T) {
	var first, second int
	refuse := `{"error":{"message":"model \"gpt-6-luna\" is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model"}}`
	a := streamStatusCounter(http.StatusBadRequest, refuse, &first)
	b := streamStatusCounter(0, "", &second)
	g := twoAccountGateway(t, a, b)

	out := &strings.Builder{}
	account, err := g.ChatStream(context.Background(), []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`), out)
	if err != nil {
		t.Fatalf("protocol refusal stopped the turn instead of failing over: %v", err)
	}
	if first == 0 || second == 0 {
		t.Fatalf("attempts first=%d second=%d", first, second)
	}
	if account != "acct-b" {
		t.Fatalf("served by %q, want the account that can answer", account)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("the client did not get the healthy account's answer: %s", out.String())
	}
	for _, cd := range g.Cooldowns() {
		if cd.AccountID == "acct-a" {
			t.Errorf("a protocol refusal cooled the account: %+v", cd)
		}
	}
}

// Providers report an exhausted quota as HTTP 400 with a usage-limit phrase.
// That is a reason to try the next account, not a malformed request.
func TestChatStreamFailsOverOnAUsageLimit400(t *testing.T) {
	var first, second int
	limit := `{"error":{"message":"You have reached your usage limit","type":"invalid_request_error"}}`
	a := streamStatusCounter(http.StatusBadRequest, limit, &first)
	b := streamStatusCounter(0, "", &second)
	g := twoAccountGateway(t, a, b)

	out := &strings.Builder{}
	if _, err := g.ChatStream(context.Background(), []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`), out); err != nil {
		t.Fatalf("a usage-limit 400 stopped the turn instead of failing over: %v", err)
	}
	if second == 0 {
		t.Error("the healthy account was never used")
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("the client did not get the healthy account's answer: %s", out.String())
	}
}

func TestClaudeStreamFailsOverWhenTheModelRefusesTheProtocol(t *testing.T) {
	var first, second int
	refuse := `{"error":{"message":"model \"claude-opus-5.5\" is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model"}}`
	a := streamStatusCounter(http.StatusBadRequest, refuse, &first)
	b := streamStatusCounter(0, "", &second)
	g := twoAccountGateway(t, a, b)

	out := &strings.Builder{}
	if _, err := g.ClaudeChatStream(context.Background(), claudeBody("m"), out); err != nil {
		t.Fatalf("protocol refusal stopped the turn instead of failing over: %v", err)
	}
	if second == 0 {
		t.Error("the healthy account was never used")
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("the client did not get the healthy account's answer: %s", out.String())
	}
}

func TestResponsesStreamFailsOverWhenTheModelRefusesTheProtocol(t *testing.T) {
	var first, second int
	refuse := `{"error":{"message":"model \"gpt-6-luna\" is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model"}}`
	a := streamStatusCounter(http.StatusBadRequest, refuse, &first)
	b := streamStatusCounter(0, "", &second)
	g := twoAccountGateway(t, a, b)

	out := &strings.Builder{}
	if _, err := g.ResponsesStream(context.Background(), responsesBody("m"), out); err != nil {
		t.Fatalf("protocol refusal stopped the turn instead of failing over: %v", err)
	}
	if second == 0 {
		t.Error("the healthy account was never used")
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("the client did not get the healthy account's answer: %s", out.String())
	}
}

func TestResponsesStreamFailsOverOnA502(t *testing.T) {
	var first, second int
	a := streamStatusCounter(http.StatusBadGateway, "upstream is down", &first)
	b := streamStatusCounter(0, "", &second)
	g := twoAccountGateway(t, a, b)

	out := &strings.Builder{}
	if _, err := g.ResponsesStream(context.Background(), responsesBody("m"), out); err != nil {
		t.Fatalf("a 502 from one account was returned instead of failing over: %v", err)
	}
	if first == 0 {
		t.Error("the failing account was never tried")
	}
	if second == 0 {
		t.Error("the healthy account was never used")
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("the client did not get the healthy account's answer: %s", out.String())
	}
}

func TestClaudeStreamFailsOverOnA502(t *testing.T) {
	var first, second int
	a := streamStatusCounter(http.StatusBadGateway, "upstream is down", &first)
	b := streamStatusCounter(0, "", &second)
	g := twoAccountGateway(t, a, b)

	out := &strings.Builder{}
	if _, err := g.ClaudeChatStream(context.Background(), claudeBody("m"), out); err != nil {
		t.Fatalf("a 502 from one account was returned instead of failing over: %v", err)
	}
	if second == 0 {
		t.Error("the healthy account was never used")
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("the client did not get the healthy account's answer: %s", out.String())
	}
}

// #50: an error chunk arriving before any output is a failed attempt, not a
// turn to hand back. The client has seen nothing, so another account can take
// it -- and the failing account must not be credited with a success.
func TestTranslatedStreamErrorChunkBeforeOutputFailsOver(t *testing.T) {
	var first, second int
	a := streamErrorChunkCounter(&first)
	b := streamStatusCounter(0, "", &second)
	g := twoAccountGateway(t, a, b)

	out := &strings.Builder{}
	if _, err := g.ResponsesStream(context.Background(), responsesBody("m"), out); err != nil {
		t.Fatalf("an early error chunk was returned instead of failing over: %v", err)
	}
	if second == 0 {
		t.Error("the healthy account was never used")
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("the client did not get the healthy account's answer: %s", out.String())
	}
}

// Once bytes have reached the client the turn is committed: no failover, and the
// error is what the client gets. This is the rule the early-error-chunk fix
// must not overreach past.
func TestTranslatedStreamDoesNotFailOverAfterOutput(t *testing.T) {
	var first, second int
	a := streamErrorAfterOutputCounter(&first)
	b := streamStatusCounter(0, "", &second)
	g := twoAccountGateway(t, a, b)

	out := &strings.Builder{}
	_, err := g.ResponsesStream(context.Background(), responsesBody("m"), out)
	if err == nil {
		t.Fatal("expected the error to be returned after output was committed")
	}
	if second != 0 {
		t.Error("failed over after output had already reached the client")
	}
}

func streamStatusCounter(status int, body string, hits *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		if status != 0 {
			*hits++
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		*hits++
		streamOnce(w)
	}
}

// streamErrorChunkCounter sends a data: error chunk and nothing else, which the
// adapter reports as a clean EOF.
func streamErrorChunkCounter(hits *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		*hits++
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"upstream refused\",\"type\":\"server_error\"}}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// streamErrorAfterOutputCounter sends real output and then an error chunk.
func streamErrorAfterOutputCounter(hits *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		*hits++
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"upstream died\",\"type\":\"server_error\"}}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
}
