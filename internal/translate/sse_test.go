package translate

import (
	"bytes"
	"strings"
	"testing"
)

func TestOpenAISSEToClaudeTrueEvents(t *testing.T) {
	in := strings.NewReader("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\ndata: [DONE]\n\n")
	var out bytes.Buffer
	if err := OpenAISSEToClaude(in, &out, "m"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, ev := range []string{"event: message_start", "event: content_block_start", "event: content_block_delta", "event: content_block_stop", "event: message_delta", "event: message_stop"} {
		if !strings.Contains(got, ev) {
			t.Fatalf("missing %s in %s", ev, got)
		}
	}
	if strings.Contains(got, "event: message\n") {
		t.Fatal("must not wrap as a single event: message")
	}
	if !strings.Contains(got, `"text":"hel"`) || !strings.Contains(got, `"text":"lo"`) {
		t.Fatalf("%s", got)
	}
}

func TestOpenAISSEToClaudeEmitsThinkingBeforeStop(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		`data: {"id":"c1","model":"m","choices":[{"delta":{"content":"hello"}}]}`,
		``,
		`data: {"choices":[{"delta":{"reasoning_opaque":[{"kind":"anthropic_thinking","thinking":"secret chain","signature":"sig_1"},{"kind":"responses_reasoning","id":"rs_9","encrypted_content":"enc"}]}}]}`,
		``,
		`data: {"choices":[{"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n"))
	var out bytes.Buffer
	if err := OpenAISSEToClaude(in, &out, "m"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, `"text":"secret chain"`) || strings.Contains(got, "rs_9") || strings.Contains(got, `"encrypted_content"`) {
		t.Fatalf("opaque leaked as text or responses kind: %s", got)
	}
	think := strings.Index(got, `"type":"thinking"`)
	stop := strings.Index(got, "event: message_stop")
	if think < 0 || stop < 0 || think > stop {
		t.Fatalf("thinking block missing before message_stop: %s", got)
	}
	if !strings.Contains(got, `"signature":"sig_1"`) || !strings.Contains(got, `"thinking":"secret chain"`) {
		t.Fatalf("signature not restored: %s", got)
	}
}

func TestOpenAISSEToClaudeEmptyWritesNothing(t *testing.T) {
	var out bytes.Buffer
	if err := OpenAISSEToClaude(strings.NewReader(""), &out, "m"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %s", out.Bytes())
	}
}

func TestClaudeSSEToOpenAI(t *testing.T) {
	in := strings.NewReader("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"model\":\"c\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
	var out bytes.Buffer
	if err := ClaudeSSEToOpenAI(in, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `"content":"hi"`) {
		t.Fatalf("%s", got)
	}
	if !strings.Contains(got, `"role":"assistant"`) {
		t.Fatalf("missing initial assistant role chunk: %s", got)
	}
	assertChatSSEFinish(t, got, "stop")
}

func TestClaudeSSEToOpenAIToolUseEmitsFinishReasonToolCalls(t *testing.T) {
	in := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_tool","model":"c"}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")
	var out bytes.Buffer
	if err := ClaudeSSEToOpenAI(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	assertChatSSEFinish(t, out.String(), "tool_calls")
}

func sseDataPayloads(s string) []string {
	var payloads []string
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payloads = append(payloads, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	return payloads
}

func assertChatSSEFinish(t *testing.T, sse, reason string) {
	t.Helper()
	payloads := sseDataPayloads(sse)
	if len(payloads) < 2 {
		t.Fatalf("want finish chunk then [DONE], got %d payloads: %q", len(payloads), sse)
	}
	if got := payloads[len(payloads)-1]; got != "[DONE]" {
		t.Fatalf("last payload = %q, want [DONE]\n%s", got, sse)
	}
	finish := payloads[len(payloads)-2]
	want := `"finish_reason":"` + reason + `"`
	if !strings.Contains(finish, want) {
		t.Fatalf("finish chunk missing %s: %s", want, finish)
	}
}
