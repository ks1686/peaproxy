package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

// A content filter can stop a turn inside a tool call. The client must see a
// refusal, not a tool handoff it would run with the partial arguments.
func TestOpenAISSEToClaudeContentFilterDuringToolUse(t *testing.T) {
	// Given a stream the content filter stopped mid tool call.
	in := strings.Join([]string{
		`data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"path\":\"a.t"}}]}}]}`,
		`data: {"choices":[{"finish_reason":"content_filter"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	var out strings.Builder

	// When it is translated.
	if err := OpenAISSEToClaude(strings.NewReader(in), &out, "m"); err != nil {
		t.Fatal(err)
	}

	// Then the turn ends as a refusal.
	var delta struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(singleClaudeEvent(t, out.String(), "message_delta"), &delta); err != nil {
		t.Fatal(err)
	}
	if delta.Delta.StopReason != "refusal" {
		t.Fatalf("stop_reason %q, want refusal so the client does not run a blocked call:\n%s", delta.Delta.StopReason, out.String())
	}
}

func TestFromOpenAIContentFilterIsRefusal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
	}{
		{"blocked text", `{"role":"assistant","content":"par"}`},
		{"blocked inside a tool call", `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"path\":\"a.t"}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a completion the content filter stopped.
			in := []byte(`{"id":"chatcmpl-1","model":"m","choices":[{"message":` + tc.message + `,"finish_reason":"content_filter"}]}`)

			// When it is translated.
			out, err := FromOpenAI(in, "m")

			// Then the message ends as a refusal.
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				StopReason string `json:"stop_reason"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if got.StopReason != "refusal" {
				t.Fatalf("stop_reason %q, want refusal: %s", got.StopReason, out)
			}
		})
	}
}
