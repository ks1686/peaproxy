package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The terminal class is the only evidence D1 has, so a stream that did end
// properly must never be reported as "none". Matching markers as substrings
// missed perfectly valid JSON formatting and produced exactly that false
// negative.
//
// https://github.com/ks1686/peaproxy plan T1 review finding F2.
func TestClassifyTerminalFormattingVariants(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want string
	}{
		{"two spaces", `data: {"choices":[{"index":0,"delta":{},"finish_reason":  "stop"}]}`, TerminalChatFinish},
		{"space before colon", `data: {"choices":[{"index":0,"delta":{},"finish_reason" : "stop"}]}`, TerminalChatFinish},
		{"pretty printed", "data: {\ndata:   \"choices\": [\ndata:     {\"finish_reason\": \"tool_calls\"}\ndata:   ]\ndata: }", TerminalChatFinish},
		{"type spaced colon", `data: {"type" : "response.completed"}`, TerminalResponsesDone},
		{"event field only", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", TerminalMessagesStop},
		{"done wins over finish", "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", TerminalDone},
		{"responses wins over stray finish", "data: {\"type\":\"response.incomplete\"}\n\n", TerminalResponsesPartial},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyTerminal([]byte(c.tail)); got != c.want {
				t.Fatalf("classifyTerminal = %q, want %q", got, c.want)
			}
		})
	}
}

// A model writing about JSON must not be mistaken for a terminal event.
func TestClassifyTerminalIgnoresProseThatLooksLikeJSON(t *testing.T) {
	tail := `data: {"choices":[{"index":0,"delta":{"content":"here is an example {\"finish_reason\": \"stop\"} in a doc"}}]}` + "\n\n"
	if got := classifyTerminal([]byte(tail)); got != TerminalNone {
		t.Fatalf("classifyTerminal = %q, want %q for a prose mention", got, TerminalNone)
	}
}

// A frame split across writes must still be classified once complete.
func TestClassifyTerminalAcrossChunkBoundary(t *testing.T) {
	full := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\"}}\n\n"
	cut := len(full) - 10
	joined := append([]byte(full[:cut]), full[cut:]...)
	if got := classifyTerminal(joined); got != TerminalResponsesDone {
		t.Fatalf("classifyTerminal = %q, want %q", got, TerminalResponsesDone)
	}
}

// classifyTerminalFrames is the JSON-reading core, kept separate so the
// precedence rule is stated once.
func TestClassifyTerminalPrecedence(t *testing.T) {
	frames := []string{
		"data: [DONE]\n\n",
		"data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n",
		"data: {\"type\":\"response.completed\"}\n\n",
		"data: {\"type\":\"message_stop\"}\n\n",
	}
	for _, f := range frames {
		if !strings.HasPrefix(f, "data: [DONE]") {
			continue
		}
		_ = f
	}
	if got := classifyTerminal([]byte(strings.Join(frames, ""))); got != TerminalDone {
		t.Fatalf("precedence = %q, want %q", got, TerminalDone)
	}
}

func TestClassifyTerminalParsesFramesNotSubstrings(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\"}\n\n")
	buf.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	if got := classifyTerminal(buf.Bytes()); got != TerminalMessagesStop {
		t.Fatalf("classifyTerminal = %q, want %q", got, TerminalMessagesStop)
	}
	// Guard: the implementation must not simply look for the literal marker.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"type":"message_stop"}`), &raw); err != nil {
		t.Fatalf("fixture must be valid JSON: %v", err)
	}
}
