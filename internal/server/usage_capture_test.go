package server

import (
	"net/http/httptest"
	"testing"

	"github.com/ks1686/peaproxy/internal/usage"
)

// A native Claude stream is relayed with io.Copy, so message_start (input and
// cache counters) and the final message_delta (output tokens) usually reach the
// writer in different Write calls, and a frame can be split across two calls.
func TestSseWriterKeepsUsageFromEveryWrite(t *testing.T) {
	w := &sseWriter{ResponseWriter: httptest.NewRecorder()}
	writes := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1200,\"cache_read_input_tokens\":900,\"output_tokens\":1}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_",
		"tokens\":42}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	for _, p := range writes {
		if _, err := w.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	var e usage.Event
	usage.ApplyPublishedUsage(&e, w.usageBody(), false)
	if e.PromptTokens != 1200 || e.CacheRead != 900 || e.CompletionTokens != 42 {
		t.Fatalf("usage across writes = prompt %d cacheRead %d completion %d, want 1200/900/42", e.PromptTokens, e.CacheRead, e.CompletionTokens)
	}
	if !e.Costable {
		t.Fatal("a stream that published both sides of usage must be costable")
	}
}

func TestSseWriterUsageMemoryIsBounded(t *testing.T) {
	w := &sseWriter{ResponseWriter: httptest.NewRecorder()}
	frame := []byte("data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n")
	for i := 0; i < 10000; i++ {
		if _, err := w.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(w.usageBody()); n > 64*len(frame) {
		t.Fatalf("usage capture grew to %d bytes over a long stream", n)
	}
}
