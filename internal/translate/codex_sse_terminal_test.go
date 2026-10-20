package translate

import (
	"bytes"
	"strings"
	"testing"
)

func codexStream(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String()
}

func TestResponsesSSEFailedIsNotReportedAsAFinishedAnswer(t *testing.T) {
	in := codexStream(
		`{"type":"response.output_text.delta","delta":"partial"}`,
		`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"upstream exploded"}}}`,
	)
	var out bytes.Buffer
	err := ResponsesSSEToOpenAI(strings.NewReader(in), &out, "gpt-6-astra")
	if err == nil {
		t.Fatalf("a failed Responses stream must return an error, got output:\n%s", out.String())
	}
	if strings.Contains(out.String(), `"finish_reason":"stop"`) {
		t.Fatalf("a failed stream must not end with finish_reason stop:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "upstream exploded") {
		t.Fatalf("the client must see the upstream failure message:\n%s", out.String())
	}
}

func TestResponsesSSEErrorEventIsNotReportedAsAFinishedAnswer(t *testing.T) {
	in := codexStream(`{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`)
	var out bytes.Buffer
	if err := ResponsesSSEToOpenAI(strings.NewReader(in), &out, "gpt-6-astra"); err == nil {
		t.Fatalf("an error event must return an error:\n%s", out.String())
	}
	if strings.Contains(out.String(), `"finish_reason"`) {
		t.Fatalf("an error event must not produce a finish chunk:\n%s", out.String())
	}
}

func TestResponsesSSEIncompleteMapsToLength(t *testing.T) {
	in := codexStream(
		`{"type":"response.output_text.delta","delta":"cut off"}`,
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`,
	)
	var out bytes.Buffer
	if err := ResponsesSSEToOpenAI(strings.NewReader(in), &out, "gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	assertChatSSEFinish(t, out.String(), "length")
}

func TestResponsesSSEIncompleteContentFilter(t *testing.T) {
	in := codexStream(`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`)
	var out bytes.Buffer
	if err := ResponsesSSEToOpenAI(strings.NewReader(in), &out, "gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	assertChatSSEFinish(t, out.String(), "content_filter")
}

func TestResponsesSSEReasoningDeltasDoNotLeakIntoContent(t *testing.T) {
	in := codexStream(
		`{"type":"response.reasoning_summary_text.delta","delta":"SECRET-THOUGHT"}`,
		`{"type":"response.reasoning_text.delta","delta":"RAW-THOUGHT"}`,
		`{"type":"response.output_text.delta","delta":"answer"}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":3,"output_tokens":2}}}`,
	)
	var out bytes.Buffer
	if err := ResponsesSSEToOpenAI(strings.NewReader(in), &out, "gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if strings.Contains(s, "SECRET-THOUGHT") || strings.Contains(s, "RAW-THOUGHT") {
		t.Fatalf("reasoning text leaked into the chat content:\n%s", s)
	}
	if !strings.Contains(s, `"content":"answer"`) {
		t.Fatalf("answer text lost:\n%s", s)
	}
	assertChatSSEFinish(t, s, "stop")
}

// A buffered Responses stream that never reached a terminal event was cut off
// (an 8 MiB read cap, a dropped connection). Its partial text must not be
// handed back as a finished answer.
func TestResponsesStreamToJSONRefusesAStreamWithoutATerminalEvent(t *testing.T) {
	in := codexStream(
		`{"type":"response.created","response":{"id":"r"}}`,
		`{"type":"response.output_text.delta","delta":"the first half of an ans"}`,
	)
	if out, err := ResponsesStreamToJSON([]byte(in)); err == nil {
		t.Fatalf("truncated stream returned as success: %s", out)
	}
}
