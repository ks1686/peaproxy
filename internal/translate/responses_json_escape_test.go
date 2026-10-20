package translate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The .done events were formatted with Go's %q, whose escapes (\x1b, \v,
// \U000e0067) are not JSON. Every event a Responses client receives must parse.
func TestOpenAISSEToResponsesEmitsValidJSONForControlAndAstralText(t *testing.T) {
	content, _ := json.Marshal("red \x1b[31mtext\x1b[0m tab\v flag \U0001F3F4\U000E0067\U000E0062\U000E007F")
	args, _ := json.Marshal("{\"q\":\"a\vb\"}")
	in := "data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"content\":" + string(content) + "}}]}\n\n" +
		"data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"ping\",\"arguments\":" + string(args) + "}}]}}]}\n\n" +
		"data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	var out bytes.Buffer
	if err := OpenAISSEToResponses(strings.NewReader(in), &out, "m"); err != nil {
		t.Fatal(err)
	}
	for _, p := range sseDataPayloads(out.String()) {
		if p == "[DONE]" {
			continue
		}
		if !json.Valid([]byte(p)) {
			t.Fatalf("invalid JSON event: %s", p)
		}
	}
	if !strings.Contains(out.String(), "response.output_text.done") {
		t.Fatalf("no output_text.done event to check:\n%s", out.String())
	}
}

// An accumulator places a function_call_arguments.delta into the item that
// output_item.added created; a delta for an item it has not seen is an error.
// Upstreams that send a whole call in one chunk, or arguments before the call's
// id and name, must still produce added before any delta, and the arguments an
// accumulator rebuilds must be exactly the call's.
func TestOpenAISSEToResponsesAnnouncesToolItemBeforeArgumentDeltas(t *testing.T) {
	chunks := []string{
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"ping","arguments":"{\"a\":"}}]}}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"early\":"}}]}}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"pong","arguments":"1}"}}]}}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"2}"}}]}}]}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	var in strings.Builder
	for _, c := range chunks {
		in.WriteString("data: " + c + "\n\n")
	}
	in.WriteString("data: [DONE]\n\n")
	var out bytes.Buffer
	if err := OpenAISSEToResponses(strings.NewReader(in.String()), &out, "m"); err != nil {
		t.Fatal(err)
	}
	args := map[string]string{}
	for _, p := range sseDataPayloads(out.String()) {
		var ev struct {
			Type   string `json:"type"`
			ItemID string `json:"item_id"`
			Delta  string `json:"delta"`
			Item   *struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Arguments string `json:"arguments"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(p), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.output_item.added":
			if ev.Item != nil && ev.Item.Type == "function_call" {
				args[ev.Item.ID] = ev.Item.Arguments
			}
		case "response.function_call_arguments.delta":
			if _, ok := args[ev.ItemID]; !ok {
				t.Fatalf("delta for %s before its output_item.added:\n%s", ev.ItemID, out.String())
			}
			args[ev.ItemID] += ev.Delta
		}
	}
	if args["fc_0"] != `{"a":2}` || args["fc_1"] != `{"early":1}` {
		t.Fatalf("accumulated arguments = %v, want fc_0 {\"a\":2} and fc_1 {\"early\":1}", args)
	}
}
