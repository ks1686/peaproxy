package translate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type goldenChatMsg struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
}

func parseChatMessages(t *testing.T, body []byte) []goldenChatMsg {
	t.Helper()
	var parsed struct {
		Messages []goldenChatMsg `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("chat messages: %v\n%s", err, body)
	}
	return parsed.Messages
}

func chatRoles(msgs []goldenChatMsg) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}

func assertRoles(t *testing.T, msgs []goldenChatMsg, want ...string) {
	t.Helper()
	got := chatRoles(msgs)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("roles %v, want %v", got, want)
	}
}

func assertToolAdjacent(t *testing.T, msgs []goldenChatMsg) {
	t.Helper()
	for i, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		if i == 0 {
			t.Fatalf("tool message must follow assistant tool_calls, got first: %s", mustJSON(msgs))
		}
		prev := msgs[i-1]
		if prev.Role != "assistant" && prev.Role != "tool" {
			t.Fatalf("tool role adjacency broken at %d (%s then tool): %s", i, prev.Role, mustJSON(msgs))
		}
		if prev.Role == "assistant" && len(bytesTrim(prev.ToolCalls)) == 0 {
			t.Fatalf("tool message not adjacent to assistant tool_calls at %d: %s", i, mustJSON(msgs))
		}
		if m.ToolCallID == "" {
			t.Fatalf("tool message missing tool_call_id at %d: %s", i, mustJSON(msgs))
		}
	}
}

func bytesTrim(raw json.RawMessage) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}
	return string(b)
}

func parseClaudeMessages(t *testing.T, body []byte) []struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
} {
	t.Helper()
	var parsed struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("claude messages: %v\n%s", err, body)
	}
	return parsed.Messages
}

func claudeBlockTypes(t *testing.T, content json.RawMessage) []string {
	t.Helper()
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		t.Fatalf("claude content blocks: %v (%s)", err, content)
	}
	out := make([]string, len(blocks))
	for i, b := range blocks {
		out[i] = b.Type
	}
	return out
}
