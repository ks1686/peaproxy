package translate

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// errUpstreamCut is an upstream connection that breaks in the middle of a turn.
var errUpstreamCut = errors.New("upstream connection reset")

// halfToolCall is an OpenAI chat stream cut off inside a tool call: the call
// has its id, name, and part of its arguments, then the read fails with no
// finish chunk or [DONE].
func halfToolCall() io.Reader {
	chunk := `data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"path\":\"a.t"}}]}}]}` + "\n\n"
	return io.MultiReader(strings.NewReader(chunk), iotest.ErrReader(errUpstreamCut))
}

// A failed upstream must not reach an Anthropic client as a finished tool
// turn, or the client runs the call with truncated arguments.
func TestOpenAISSEToClaudeUpstreamFailureNeverFinishesTheTurn(t *testing.T) {
	// Given an upstream that fails after half a tool call.
	var out strings.Builder

	// When the stream is translated.
	err := OpenAISSEToClaude(halfToolCall(), &out, "m")

	// Then the failure is returned and no tool_use block or turn end is sent.
	if !errors.Is(err, errUpstreamCut) {
		t.Fatalf("err = %v, want the upstream failure", err)
	}
	for _, deny := range []string{`"type":"tool_use"`, "event: message_delta", "event: message_stop"} {
		if strings.Contains(out.String(), deny) {
			t.Fatalf("failed upstream reached the client as a finished turn (%s):\n%s", deny, out.String())
		}
	}
}

// Anthropic's stream error event is how a Messages client learns a started
// stream failed, rather than reading the cut-off stream as a partial answer.
func TestOpenAISSEToClaudeUpstreamFailureEndsWithErrorEvent(t *testing.T) {
	// Given an upstream that fails after half a tool call.
	var out strings.Builder

	// When the stream is translated.
	if err := OpenAISSEToClaude(halfToolCall(), &out, "m"); !errors.Is(err, errUpstreamCut) {
		t.Fatalf("err = %v, want the upstream failure", err)
	}

	// Then the last event the client receives is an Anthropic api_error.
	events := strings.Split(strings.TrimSuffix(out.String(), "\n\n"), "\n\n")
	name, data, _ := strings.Cut(events[len(events)-1], "\n")
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &body); err != nil {
		t.Fatalf("last event data: %v\n%s", err, out.String())
	}
	if name != "event: error" || body.Type != "error" || body.Error.Type != "api_error" {
		t.Fatalf("last event %q type=%q error.type=%q, want an api_error event:\n%s", name, body.Type, body.Error.Type, out.String())
	}
}

// A failed upstream must not reach a Responses client as response.completed,
// whose output would hand it the truncated function_call to run.
func TestOpenAISSEToResponsesUpstreamFailureNeverCompletes(t *testing.T) {
	// Given an upstream that fails after half a tool call.
	var out strings.Builder

	// When the stream is translated.
	err := OpenAISSEToResponses(halfToolCall(), &out, "m")

	// Then the failure is returned and response.completed is never sent.
	if !errors.Is(err, errUpstreamCut) {
		t.Fatalf("err = %v, want the upstream failure", err)
	}
	if strings.Contains(out.String(), "response.completed") {
		t.Fatalf("failed upstream reached the client as a completed response:\n%s", out.String())
	}
}
