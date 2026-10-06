package contextopt

import (
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/contextstore"
)

func storeOf(t *testing.T, arts map[string]string) *contextstore.Store {
	t.Helper()
	s := contextstore.New(contextstore.Options{})
	for k, v := range arts {
		if err := s.Put("s1", contextstore.Artifact{Key: k, Body: []byte(v)}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// A response with no proxy tool call must be returned to the caller untouched.
// Treating an ordinary answer as a search request would corrupt it.
func TestNoToolCallMeansNothingToExecute(t *testing.T) {
	plain := `{"choices":[{"message":{"content":"hello"}}]}`
	if got := ProxyToolCalls([]byte(plain)); len(got) != 0 {
		t.Fatalf("plain answer reported tool calls: %+v", got)
	}
}

// The caller's own tools must never be mistaken for PeaProxy's. Answering a
// tool call PeaProxy does not own would inject results the client never asked
// for into a slot it expects to fill itself.
func TestCallerToolCallsAreNotOursToAnswer(t *testing.T) {
	resp := `{"choices":[{"message":{"tool_calls":[
		{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}
	]}}]}`
	if got := ProxyToolCalls([]byte(resp)); len(got) != 0 {
		t.Fatalf("a caller tool call was claimed by PeaProxy: %+v", got)
	}
}

func TestProxyToolCallIsRecognised(t *testing.T) {
	resp := `{"choices":[{"message":{"tool_calls":[
		{"id":"call_9","type":"function","function":{"name":"pea_search","arguments":"{\"query\":\"staging port\"}"}}
	]}}]}`
	calls := ProxyToolCalls([]byte(resp))
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].ID != "call_9" {
		t.Errorf("id = %q", calls[0].ID)
	}
	if calls[0].Query != "staging port" {
		t.Errorf("query = %q", calls[0].Query)
	}
}

// A search must actually search, and must say nothing rather than invent an
// answer when it finds nothing.
func TestSearchReturnsPassagesOrNothing(t *testing.T) {
	s := storeOf(t, map[string]string{"k": "the staging cluster runs on port 8443"})

	if got := Search(s, "s1", "staging cluster port"); !strings.Contains(got, "8443") {
		t.Fatalf("search did not find the passage: %q", got)
	}
	if got := Search(s, "s1", "quarterly revenue forecast"); got != "" {
		t.Fatalf("search invented something for an unrelated query: %q", got)
	}
	if got := Search(s, "s1", "staging"); got == "" {
		t.Fatal("search found nothing for a matching query")
	}
}

// The tool result goes back into the conversation as a tool message, so the
// model sees it the way it expects, and the caller's original messages are
// still there.
func TestAppendingResultsProducesAToolMessage(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"where is staging"}]}`)
	out := AppendToolResults(body, []ToolCall{{ID: "call_9"}}, map[string]string{"call_9": "staging is on 8443"})

	if !strings.Contains(string(out), "call_9") {
		t.Fatalf("the call id is missing: %s", out)
	}
	if !strings.Contains(string(out), "8443") {
		t.Fatalf("the result is missing: %s", out)
	}
	if !strings.Contains(string(out), "where is staging") {
		t.Fatalf("the caller's message was lost: %s", out)
	}
	if !strings.Contains(string(out), `"role":"tool"`) {
		t.Fatalf("no tool message was appended: %s", out)
	}
}

// A body that cannot be parsed must be returned as it was. Returning an
// unparseable body here would silently drop the whole conversation.
func TestAppendingResultsLeavesUnusableBodiesAlone(t *testing.T) {
	body := []byte(`{"messages":`)
	out := AppendToolResults(body, []ToolCall{{ID: "a"}}, map[string]string{"a": "x"})
	if string(out) != string(body) {
		t.Fatalf("unparseable body was rewritten: %s", out)
	}
}

// Streaming cannot use the tool path. Answering a tool call mid-stream would
// mean buffering the response, which breaks the incremental delivery the client
// asked for. Streaming requests fall back to pre-retrieval instead.
func TestStreamingRequestsAreNotOfferedTheTool(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","function":{"name":"t"}}],"messages":[]}`)
	if _, injected := Inject(body, Plan{
		Enabled: true, ClientTools: true, ProviderTools: true, Streaming: true,
	}); injected {
		t.Fatal("a streaming request was offered a tool PeaProxy could only answer by buffering")
	}
}
