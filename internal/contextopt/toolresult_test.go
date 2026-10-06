package contextopt

import (
	"encoding/json"
	"strings"
	"testing"
)

// A tool result is only valid when the assistant turn that asked for it is in
// the conversation. OpenAI-compatible servers reject a tool message whose
// tool_call_id has no preceding assistant tool call, so the second round of a
// search was rejected by exactly the servers most likely to be strict.
func TestToolResultsCarryTheAssistantTurnThatAskedForThem(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"what did I decide?"}]}`)
	calls := []ToolCall{{ID: "call_1", Query: "decision"}}

	out := AppendToolResults(body, calls, map[string]string{"call_1": "ship it"})
	if string(out) == string(body) {
		t.Fatal("nothing was appended")
	}

	var doc struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
			Content    string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("appended body is not valid JSON: %v", err)
	}

	last := doc.Messages[len(doc.Messages)-1]
	if last.Role != "tool" || last.ToolCallID != "call_1" {
		t.Fatalf("the result is not a tool message for call_1: %+v", last)
	}
	if prev := doc.Messages[len(doc.Messages)-2]; prev.Role != "assistant" || len(prev.ToolCalls) != 1 {
		t.Fatalf("no assistant tool-call turn precedes the result: %+v", prev)
	}
	if got := doc.Messages[len(doc.Messages)-2].ToolCalls[0]; got.ID != "call_1" || got.Function.Name != ToolName {
		t.Fatalf("the assistant turn does not carry the call: %+v", got)
	}
	// The caller's own messages must survive, in order, ahead of the appended
	// pair.
	if doc.Messages[0].Role != "user" || !strings.Contains(doc.Messages[0].Content, "what did I decide?") {
		t.Fatalf("the caller's first message was altered: %+v", doc.Messages[0])
	}
}

// Several proxy calls in one turn become one assistant turn carrying all of
// them, then one tool message each. Two assistant turns claiming the same call
// would be its own protocol error.
func TestSeveralCallsShareOneAssistantTurn(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"q"}]}`)
	calls := []ToolCall{{ID: "a", Query: "one"}, {ID: "b", Query: "two"}}

	out := AppendToolResults(body, calls, map[string]string{"a": "1", "b": "2"})
	var doc struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	assistants := 0
	for _, m := range doc.Messages {
		if m.Role == "assistant" {
			assistants++
			if len(m.ToolCalls) != 2 {
				t.Fatalf("the assistant turn carries %d calls, want 2", len(m.ToolCalls))
			}
		}
	}
	if assistants != 1 {
		t.Fatalf("%d assistant turns appended, want 1", assistants)
	}
	if n := len(doc.Messages); n != 4 {
		t.Fatalf("%d messages, want user + assistant + two results", n)
	}
}

// The arguments are sent back as a JSON object, not the raw string the model
// wrote. A server that parses tool_call arguments strictly rejects a string.
func TestAppendedCallCarriesObjectArguments(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"q"}]}`)
	out := AppendToolResults(body, []ToolCall{{ID: "a", Query: "needle"}}, map[string]string{"a": "1"})
	if !strings.Contains(string(out), `"arguments":{"query":"needle"}`) {
		t.Fatalf("arguments were not sent as an object: %s", out)
	}
}

// A body that cannot be parsed is returned untouched, as before. Inventing a
// conversation here would drop the caller's whole history.
func TestAppendOnAnUnparseableBodyChangesNothing(t *testing.T) {
	body := []byte(`{"model":`)
	out := AppendToolResults(body, []ToolCall{{ID: "a"}}, map[string]string{"a": "1"})
	if string(out) != string(body) {
		t.Fatalf("an unparseable body was rewritten: %s", out)
	}
}
