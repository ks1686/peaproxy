package server

import (
	"net/http/httptest"
	"testing"
)

func TestClassifyTerminal(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want string
	}{
		{"empty", "", TerminalNone},
		{"openai done", `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n", TerminalDone},
		{"openai finish without done", "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n", TerminalChatFinish},
		{"length", "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n", TerminalChatFinish},
		{"null finish is not a finish", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n", TerminalNone},
		{"pretty printed finish", "data: {\n  \"choices\": [{\"finish_reason\": \"stop\"}]\n}\n\n", TerminalChatFinish},
		{"responses completed", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1}}}\n\n", TerminalResponsesDone},
		{"responses incomplete", "event: response.incomplete\ndata: {\"type\":\"response.incomplete\"}\n\n", TerminalResponsesPartial},
		{"responses failed", "data: {\"type\":\"response.failed\"}\n\n", TerminalResponsesPartial},
		{"claude message_stop", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", TerminalMessagesStop},
		{"mid turn only", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n", TerminalNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyTerminal([]byte(c.tail)); got != c.want {
				t.Fatalf("classifyTerminal = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSseWriterKeepsBoundedTailAndTerminal(t *testing.T) {
	w := &sseWriter{ResponseWriter: httptest.NewRecorder()}
	long := make([]byte, 40<<10)
	for i := range long {
		long[i] = 'x'
	}
	_, _ = w.Write(long)
	_, _ = w.Write([]byte("data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	if len(w.tail) > terminalTailBytes {
		t.Fatalf("tail grew to %d bytes; it must stay bounded", len(w.tail))
	}
	if got := w.terminal(); got != TerminalDone {
		t.Fatalf("terminal = %q, want %q", got, TerminalDone)
	}
}

func TestSseWriterTerminalNoneWhenStreamCuts(t *testing.T) {
	w := &sseWriter{ResponseWriter: httptest.NewRecorder()}
	_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"half\"}}]}\n\n"))
	if got := w.terminal(); got != TerminalNone {
		t.Fatalf("terminal = %q, want %q", got, TerminalNone)
	}
}
