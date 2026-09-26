package translate

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestResponsesChatToolsRoundTripFixtures(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		deny []string
	}{
		{
			name: "function tools and prior calls become chat tool_calls",
			in: `{
				"model":"m",
				"tools":[{"type":"function","name":"lookup","description":"find","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}],
				"tool_choice":{"type":"function","name":"lookup"},
				"input":[
					{"role":"user","content":[{"type":"input_text","text":"look"}]},
					{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"},
					{"type":"function_call_output","call_id":"call_1","output":"found"},
					{"type":"reasoning","summary":[{"type":"summary_text","text":"skip me"}]}
				]
			}`,
			want: []string{`"tools"`, `"tool_choice"`, `"tool_calls"`, `"call_1"`, `"role":"tool"`, `"tool_call_id":"call_1"`, `"found"`, `"lookup"`},
			deny: []string{"skip me", `"input"`},
		},
		{
			name: "string input stays a user message",
			in:   `{"model":"m","instructions":"be brief","input":"ping"}`,
			want: []string{`"role":"system"`, `"be brief"`, `"role":"user"`, `"ping"`, `"messages"`},
			deny: []string{`"input"`},
		},
		{
			name: "developer role maps to system",
			in:   `{"model":"m","input":[{"role":"developer","content":[{"type":"input_text","text":"sys"}]},{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			want: []string{`"role":"system"`, `"sys"`, `"role":"user"`, `"hi"`},
			deny: []string{`"developer"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, req, err := ResponsesToOpenAI([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if req.Model != "m" {
				t.Fatalf("model %s", req.Model)
			}
			for _, w := range tc.want {
				if !bytes.Contains(body, []byte(w)) {
					t.Fatalf("missing %s in %s", w, body)
				}
			}
			for _, d := range tc.deny {
				if bytes.Contains(body, []byte(d)) {
					t.Fatalf("must not contain %s: %s", d, body)
				}
			}
		})
	}
}

func TestFromOpenAIChatToolCallFixtures(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		deny []string
	}{
		{
			name: "tool_calls become function_call output items",
			in: `{
				"id":"chatcmpl-1",
				"model":"m",
				"choices":[{
					"message":{
						"role":"assistant",
						"content":null,
						"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]
					},
					"finish_reason":"tool_calls"
				}]
			}`,
			want: []string{`"type":"function_call"`, `"call_id":"call_1"`, `"name":"lookup"`, `"object":"response"`},
			deny: []string{`"output":"found"`, `"tool_calls"`},
		},
		{
			name: "plain text wraps as output_text",
			in:   `{"id":"chatcmpl-1","model":"m","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			want: []string{`"output_text":"hello"`, `"type":"output_text"`, `"status":"completed"`},
			deny: []string{`"function_call"`},
		},
		{
			name: "text plus tool_calls keeps both",
			in: `{
				"id":"chatcmpl-2",
				"model":"m",
				"choices":[{
					"message":{
						"role":"assistant",
						"content":"calling",
						"tool_calls":[{"id":"call_9","type":"function","function":{"name":"ping","arguments":"{}"}}]
					},
					"finish_reason":"tool_calls"
				}]
			}`,
			want: []string{`"function_call"`, `"call_9"`, `"ping"`, `"calling"`, `"output_text":"calling"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := FromOpenAIChat([]byte(tc.in), "m")
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.want {
				if !bytes.Contains(out, []byte(w)) {
					t.Fatalf("missing %s in %s", w, out)
				}
			}
			for _, d := range tc.deny {
				if bytes.Contains(out, []byte(d)) {
					t.Fatalf("must not contain %s: %s", d, out)
				}
			}
			var parsed struct {
				Object string `json:"object"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed.Object != "response" || parsed.Status != "completed" {
				t.Fatalf("%s", out)
			}
		})
	}
}

func TestLooksLikeResponsesTable(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{`{"model":"m","input":"hi"}`, true},
		{`{"model":"m","input":[{"role":"user","content":"hi"}]}`, true},
		{`{"model":"m","messages":[{"role":"user","content":"hi"}]}`, false},
		{`{"model":"m","input":null}`, false},
		{`{"input":"hi"}`, false},
		{`{}`, false},
	}
	for _, tc := range cases {
		if got := LooksLikeResponses([]byte(tc.raw)); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.raw, got, tc.want)
		}
	}
}

func TestResponsesToolsChatShape(t *testing.T) {
	in := []byte(`{"model":"m","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":"hi"}`)
	body, _, err := ResponsesToOpenAI(in)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tools) != 1 || parsed.Tools[0].Type != "function" || parsed.Tools[0].Function.Name != "lookup" {
		t.Fatalf("chat-shaped tools: %s", body)
	}
}
