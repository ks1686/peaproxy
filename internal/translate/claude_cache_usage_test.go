package translate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/usage"
)

func assertClaudeCacheUsage(t *testing.T, body []byte) {
	t.Helper()
	var e usage.Event
	usage.ApplyPublishedUsage(&e, body, false)
	if e.PromptTokens != 100020 || e.CacheRead != 100000 || !e.CacheReadNested || e.CacheWrite != 500 || e.CompletionTokens != 7 {
		t.Fatalf("ledger reads prompt %d cacheRead %d nested %v cacheWrite %d completion %d; want 100020/100000/true/500/7\n%s",
			e.PromptTokens, e.CacheRead, e.CacheReadNested, e.CacheWrite, e.CompletionTokens, body)
	}
}

// Anthropic reports cache reads and writes beside input_tokens. Mapping only
// input_tokens to prompt_tokens dropped a cached system prompt from the
// client's usage and from the ledger.
func TestFromClaudeCarriesCacheTokens(t *testing.T) {
	raw := []byte(`{"id":"m","model":"claude","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn",
		"usage":{"input_tokens":20,"cache_read_input_tokens":100000,"cache_creation_input_tokens":500,"output_tokens":7}}`)
	out, err := FromClaude(raw)
	if err != nil {
		t.Fatal(err)
	}
	assertClaudeCacheUsage(t, out)
}

func TestClaudeSSEToOpenAICarriesCacheTokens(t *testing.T) {
	in := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude\",\"usage\":{\"input_tokens\":20,\"cache_read_input_tokens\":100000,\"cache_creation_input_tokens\":500,\"output_tokens\":1}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	var out bytes.Buffer
	if err := ClaudeSSEToOpenAI(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	assertClaudeCacheUsage(t, out.Bytes())
}

// Chat tool_calls are indexed 0, 1, 2 by call. Using the Anthropic content
// block index (a text or thinking block comes first) left holes that
// array-accumulating clients trip over.
func TestClaudeSSEToOpenAIIndexesToolCallsFromZero(t *testing.T) {
	in := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude\",\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"a\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t2\",\"name\":\"b\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\n"
	var out bytes.Buffer
	if err := ClaudeSSEToOpenAI(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, `"index":0,"id":"t1"`) || !strings.Contains(s, `"index":1,"id":"t2"`) {
		t.Fatalf("tool calls not indexed 0 and 1:\n%s", s)
	}
}
