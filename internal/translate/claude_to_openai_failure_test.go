package translate

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

const claudeHalfToolUse = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-5-5\"}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"write\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"a.t\"}}\n\n"

func assertNoOpenAIFinish(t *testing.T, out string) {
	t.Helper()
	for _, deny := range []string{`"finish_reason":"`, "data: [DONE]"} {
		if strings.Contains(out, deny) {
			t.Fatalf("failed upstream reached the client as a finished turn (%s):\n%s", deny, out)
		}
	}
}

// A Claude stream that breaks mid tool call must not reach an OpenAI client as
// a finished turn, or the client runs the call with truncated arguments.
func TestClaudeSSEToOpenAIReadFailureNeverFinishesTheTurn(t *testing.T) {
	var out strings.Builder
	r := io.MultiReader(strings.NewReader(claudeHalfToolUse), iotest.ErrReader(errUpstreamCut))

	err := ClaudeSSEToOpenAI(r, &out)

	if !errors.Is(err, errUpstreamCut) {
		t.Fatalf("err = %v, want the upstream failure", err)
	}
	assertNoOpenAIFinish(t, out.String())
	if !strings.Contains(out.String(), `"error":`) {
		t.Fatalf("stream must end with an error chunk:\n%s", out.String())
	}
}

// Anthropic reports mid-stream failures (overloaded_error, api_error) as an
// "error" event on a 200 stream.
func TestClaudeSSEToOpenAIErrorEventFailsTheStream(t *testing.T) {
	var out strings.Builder
	in := claudeHalfToolUse + "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"

	err := ClaudeSSEToOpenAI(strings.NewReader(in), &out)

	if err == nil || !strings.Contains(err.Error(), "overloaded_error") {
		t.Fatalf("err = %v, want the upstream overloaded_error", err)
	}
	assertNoOpenAIFinish(t, out.String())
}

func TestClaudeSSEToOpenAIStopReasons(t *testing.T) {
	cases := []struct{ name, stop, want string }{
		{"max_tokens inside a tool call", "max_tokens", "length"},
		{"refusal inside a tool call", "refusal", "content_filter"},
		{"tool_use", "tool_use", "tool_calls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			in := claudeHalfToolUse + "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"" + tc.stop + "\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			if err := ClaudeSSEToOpenAI(strings.NewReader(in), &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), `"finish_reason":"`+tc.want+`"`) {
				t.Fatalf("finish_reason != %s:\n%s", tc.want, out.String())
			}
		})
	}
}

func TestFromClaudeStopReasonsWithToolCalls(t *testing.T) {
	cases := []struct{ stop, want string }{
		{"max_tokens", "length"},
		{"refusal", "content_filter"},
		{"tool_use", "tool_calls"},
	}
	for _, tc := range cases {
		t.Run(tc.stop, func(t *testing.T) {
			in := []byte(`{"id":"msg_1","model":"m","content":[{"type":"tool_use","id":"toolu_1","name":"write","input":{}}],"stop_reason":"` + tc.stop + `"}`)
			out, err := FromClaude(in)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Choices []struct {
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal(out, &body); err != nil {
				t.Fatal(err)
			}
			if got := body.Choices[0].FinishReason; got != tc.want {
				t.Fatalf("finish_reason = %q, want %q", got, tc.want)
			}
		})
	}
}
