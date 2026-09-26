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
	if !strings.Contains(out.String(), `"content":"hi"`) || !strings.Contains(out.String(), "[DONE]") {
		t.Fatalf("%s", out.String())
	}
}
