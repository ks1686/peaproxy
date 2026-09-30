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

// Responses' response.failed event is how a Responses client learns a started
// stream failed, rather than reading a bare EOF after response.created.
func TestOpenAISSEToResponsesUpstreamFailureEndsWithFailed(t *testing.T) {
	// Given an upstream that yields one valid chat chunk then fails.
	chunk := `data: {"id":"c1","model":"m","choices":[{"delta":{"content":"hi"}}]}` + "\n\n"
	in := io.MultiReader(strings.NewReader(chunk), iotest.ErrReader(errUpstreamCut))
	var out strings.Builder

	// When the stream is translated.
	err := OpenAISSEToResponses(in, &out, "m")

	// Then the upstream failure is returned, response.created was sent, and
	// the last event is response.failed (never completed or incomplete).
	if !errors.Is(err, errUpstreamCut) {
		t.Fatalf("err = %v, want the upstream failure", err)
	}
	got := out.String()
	if !strings.Contains(got, "event: response.created") {
		t.Fatalf("missing response.created:\n%s", got)
	}
	for _, deny := range []string{"response.completed", "response.incomplete"} {
		if strings.Contains(got, deny) {
			t.Fatalf("failed upstream reached the client as %s:\n%s", deny, got)
		}
	}
	events := strings.Split(strings.TrimSuffix(got, "\n\n"), "\n\n")
	name, data, _ := strings.Cut(events[len(events)-1], "\n")
	var body struct {
		Type     string `json:"type"`
		Response struct {
			Status string `json:"status"`
			Error  struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &body); err != nil {
		t.Fatalf("last event data: %v\n%s", err, got)
	}
	if name != "event: response.failed" || body.Type != "response.failed" || body.Response.Status != "failed" {
		t.Fatalf("last event %q type=%q status=%q, want response.failed:\n%s", name, body.Type, body.Response.Status, got)
	}
}

// An OpenAI-compatible upstream reports a mid-stream failure as a data chunk
// carrying a top-level error, then closes with no [DONE]. That must end a
// translated stream as a failure, not as a finished turn.
func TestOpenAIWireErrorChunkFailsTranslatedStream(t *testing.T) {
	content := `data: {"id":"c1","model":"m","choices":[{"delta":{"content":"hel"}}]}` + "\n\n"
	errObj := `data: {"error":{"message":"mock \"mid-stream\" failure","type":"server_error"}}` + "\n\n"
	errStr := `data: {"error":"mock string failure"}` + "\n\n"
	errRouter := `data: {"id":"c1","error":{"code":"server_error","message":"Provider disconnected"},"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}` + "\n\n"

	type wire struct {
		name      string
		translate func(io.Reader, io.Writer, string) error
		terminal  string
		deny      []string
	}
	wires := []wire{
		{"messages", OpenAISSEToClaude, "error", []string{`"stop_reason":"end_turn"`, "event: message_stop", "event: message_delta"}},
		{"responses", OpenAISSEToResponses, "response.failed", []string{"response.completed", "response.incomplete"}},
	}
	cases := []struct {
		name    string
		in      string
		started bool
		msg     string
	}{
		{"chunk then error object", content + errObj, true, `mock "mid-stream" failure`},
		{"chunk then error string", content + errStr, true, "mock string failure"},
		{"chunk then error with choices", content + errRouter, true, "Provider disconnected"},
		{"error before content", errObj, false, `mock "mid-stream" failure`},
	}
	for _, w := range wires {
		for _, tc := range cases {
			t.Run(w.name+"/"+tc.name, func(t *testing.T) {
				// Given an upstream stream that carries an error chunk then EOF.
				var out strings.Builder

				// When it is translated.
				err := w.translate(strings.NewReader(tc.in), &out, "m")

				// Then the upstream error is returned with its message.
				if err == nil || !strings.Contains(err.Error(), tc.msg) {
					t.Fatalf("err = %v, want an upstream stream error with %q", err, tc.msg)
				}
				got := out.String()
				if !tc.started {
					// And nothing reaches the client, so the caller can fail over.
					if got != "" {
						t.Fatalf("error before content wrote to the client:\n%s", got)
					}
					return
				}
				// And the stream ends on the wire's failure event, never a
				// finished turn.
				for _, deny := range w.deny {
					if strings.Contains(got, deny) {
						t.Fatalf("error chunk reached the client as a finished turn (%s):\n%s", deny, got)
					}
				}
				events := strings.Split(strings.TrimSuffix(got, "\n\n"), "\n\n")
				name, data, _ := strings.Cut(events[len(events)-1], "\n")
				if name != "event: "+w.terminal {
					t.Fatalf("last event %q, want %s:\n%s", name, w.terminal, got)
				}
				var body struct {
					Error    struct{ Message string } `json:"error"`
					Response struct {
						Error struct{ Message string } `json:"error"`
					} `json:"response"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &body); err != nil {
					t.Fatalf("last event data: %v\n%s", err, got)
				}
				if msg := body.Error.Message + body.Response.Error.Message; msg != tc.msg {
					t.Fatalf("failure message %q, want %q", msg, tc.msg)
				}
			})
		}
	}
}

// A Claude upstream that fails mid-stream becomes an OpenAI-wire error chunk
// from ClaudeSSEToOpenAI; a Responses client behind it must see
// response.failed.
func TestClaudeFailureThroughResponsesEndsWithFailed(t *testing.T) {
	// Given a Claude stream that errors after some text.
	claude := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude\"}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hel\"}}\n\n" +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	var chat strings.Builder
	if err := ClaudeSSEToOpenAI(strings.NewReader(claude), &chat); err == nil {
		t.Fatal("ClaudeSSEToOpenAI returned nil for a Claude error event")
	}

	// When its chat output is translated to Responses.
	var out strings.Builder
	err := OpenAISSEToResponses(strings.NewReader(chat.String()), &out, "m")

	// Then the Responses stream ends with response.failed and never completes.
	if err == nil {
		t.Fatalf("err = nil, want the upstream failure:\n%s", out.String())
	}
	got := out.String()
	if strings.Contains(got, "response.completed") {
		t.Fatalf("Claude failure reached the Responses client as completed:\n%s", got)
	}
	events := strings.Split(strings.TrimSuffix(got, "\n\n"), "\n\n")
	if name, _, _ := strings.Cut(events[len(events)-1], "\n"); name != "event: response.failed" {
		t.Fatalf("last event %q, want response.failed:\n%s", name, got)
	}
}
