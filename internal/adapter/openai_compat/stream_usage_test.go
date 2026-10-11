package openai_compat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

// Most OpenAI-compatible providers only put a usage frame in a streamed response
// when the request asks for one with stream_options.include_usage. Without it
// the stream ends cleanly, the client is happy, and PeaProxy's ledger records a
// call with no token counts at all -- which is the case a spend ceiling cannot
// measure, so it fails closed and refuses everything.
//
// The request field is the documented OpenAI one, and a provider that does not
// know it ignores it, so asking costs nothing and buys the measurement.

func streamBody(t *testing.T, raw string) []byte {
	t.Helper()
	var seen []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	a, err := New(adapter.Options{ID: "x", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := a.ChatStream(context.Background(), adapter.ChatRequest{Model: "m", Raw: []byte(raw)}, &out); err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("the upstream received no body")
	}
	return seen
}

func TestChatStreamAsksForStreamUsage(t *testing.T) {
	got := streamBody(t, `{"model":"m","stream":true,"messages":[]}`)
	if !bytes.Contains(got, []byte(`"include_usage":true`)) {
		t.Fatalf("stream request did not ask for usage: %s", got)
	}
	if !bytes.Contains(got, []byte(`"stream_options"`)) {
		t.Fatalf("include_usage was sent outside stream_options: %s", got)
	}
}

// A client that set stream_options itself made a decision about the response
// shape, and rewriting it would change a body the client owns. PeaProxy asks
// only when it was not told otherwise.
func TestChatStreamLeavesAnExplicitStreamOptionsAlone(t *testing.T) {
	got := streamBody(t, `{"model":"m","stream":true,"stream_options":{"include_usage":false},"messages":[]}`)
	var body struct {
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatalf("decode %s: %v", got, err)
	}
	if body.StreamOptions == nil || body.StreamOptions.IncludeUsage {
		t.Fatalf("an explicit stream_options was rewritten: %s", got)
	}
}

// The rest of the body has to survive the injection untouched: a dropped
// messages array turns a measurement fix into a broken request.
func TestChatStreamKeepsTheRestOfTheBodyIntact(t *testing.T) {
	raw := `{"model":"m","stream":true,"temperature":0,"messages":[{"role":"user","content":"hi"}]}`
	got := streamBody(t, raw)
	for _, want := range []string{`"model":"m"`, `"temperature":0`, `"role":"user"`, `"content":"hi"`} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatalf("body lost %s: %s", want, got)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatalf("injected body is not valid JSON: %v\n%s", err, got)
	}
}

// A non-stream request must not carry stream_options: it is meaningless there
// and some providers reject a field they do not expect on the shape they asked
// for.
func TestNonStreamRequestDoesNotCarryStreamOptions(t *testing.T) {
	var seen []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	t.Cleanup(srv.Close)
	a, err := New(adapter.Options{ID: "x", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "m", Raw: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(seen), "stream_options") {
		t.Fatalf("non-stream request carried stream_options: %s", seen)
	}
}
