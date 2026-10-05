package usage

import "testing"

// A provider's usage object is not always one document, and it is not always at
// the top level. Reading only the last top-level sample undercounted real
// spend: Anthropic reports input tokens in message_start and output tokens in
// message_delta, and the Responses wire nests usage under "response".
//
// https://github.com/ks1686/peaproxy plan T2 / defects D2 and D3.

func TestParseAnthropicStreamMergesStartAndDelta(t *testing.T) {
	body := []byte("event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":1}}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n")
	in, out, _, ok := ParsePublishedUsage(body)
	if !ok {
		t.Fatal("usage not found in an Anthropic stream")
	}
	if in != 12 {
		t.Fatalf("input = %d, want 12 (message_start)", in)
	}
	if out != 7 {
		t.Fatalf("output = %d, want 7 (cumulative message_delta)", out)
	}
}

func TestParseResponsesNestedUsage(t *testing.T) {
	body := []byte("event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":31,\"output_tokens\":9,\"total_tokens\":40}}}\n\n")
	in, out, _, ok := ParsePublishedUsage(body)
	if !ok {
		t.Fatal("usage not found under response.usage")
	}
	if in != 31 || out != 9 {
		t.Fatalf("input=%d output=%d, want 31 and 9", in, out)
	}
}

func TestParseClaudeStreamCacheCounters(t *testing.T) {
	body := []byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"cache_creation_input_tokens\":40,\"cache_read_input_tokens\":60}}}\n\n")
	in, _, _, ok := ParsePublishedUsage(body)
	if !ok {
		t.Fatal("usage not found")
	}
	if in != 100 {
		t.Fatalf("input = %d, want 100", in)
	}
	e := Event{}
	ApplyPublishedUsage(&e, body, false)
	if e.CacheWrite != 40 {
		t.Fatalf("CacheWrite = %d, want 40", e.CacheWrite)
	}
	if e.CacheRead != 60 {
		t.Fatalf("CacheRead = %d, want 60", e.CacheRead)
	}
}

func TestParseOpenAICachedTokensDetail(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":80}}}`)
	e := Event{}
	ApplyPublishedUsage(&e, body, false)
	if e.PromptTokens != 100 || e.CompletionTokens != 5 {
		t.Fatalf("tokens = %d/%d, want 100/5", e.PromptTokens, e.CompletionTokens)
	}
	if e.CacheRead != 80 {
		t.Fatalf("CacheRead = %d, want 80", e.CacheRead)
	}
}

func TestParseResponsesCachedTokensDetail(t *testing.T) {
	body := []byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":50,\"output_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":25}}}}\n\n")
	e := Event{}
	ApplyPublishedUsage(&e, body, false)
	if e.CacheRead != 25 {
		t.Fatalf("CacheRead = %d, want 25", e.CacheRead)
	}
}

// A later frame that reports zero for a counter a provider already published
// must not erase the real number.
func TestParseDoesNotEraseEarlierCounters(t *testing.T) {
	body := []byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":20}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\n")
	in, out, _, ok := ParsePublishedUsage(body)
	if !ok {
		t.Fatal("usage not found")
	}
	if in != 20 || out != 4 {
		t.Fatalf("input=%d output=%d, want 20 and 4", in, out)
	}
}
