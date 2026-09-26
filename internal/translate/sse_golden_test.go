package translate

import (
	"bytes"
	"strings"
	"testing"
)

func TestOpenAISSEToResponsesFixtures(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		deny []string
	}{
		{
			name: "text deltas emit response.output_text.delta",
			in: strings.Join([]string{
				`data: {"id":"c1","model":"m","choices":[{"delta":{"content":"hel"}}]}`,
				``,
				`data: {"choices":[{"delta":{"content":"lo"}}]}`,
				``,
				`data: [DONE]`,
				``,
				``,
			}, "\n"),
			want: []string{"event: response.created", "event: response.output_text.delta", `"delta":"hel"`, `"delta":"lo"`, "event: response.completed", `"output_text":"hello"`},
			deny: []string{"function_call", "event: message_start"},
		},
		{
			name: "tool_call argument deltas map to function_call",
			in: strings.Join([]string{
				`data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}]}}]}`,
				``,
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"x\"}"}}]}}]}`,
				``,
				`data: {"choices":[{"finish_reason":"tool_calls"}]}`,
				``,
				`data: [DONE]`,
				``,
				``,
			}, "\n"),
			want: []string{`"type":"function_call"`, `"call_id":"call_1"`, `"name":"lookup"`, `\"q\":\"x\"`, "event: response.function_call_arguments.delta", "event: response.output_item.added", "event: response.completed"},
			deny: []string{`"output":"executed"`, `"output":"found"`},
		},
		{
			name: "malformed json is skipped without inventing text",
			in: strings.Join([]string{
				`data: {"id":"c1","model":"m","choices":[{"delta":{"content":"ok"}}]}`,
				``,
				`data: {not-json`,
				``,
				`data: [DONE]`,
				``,
				``,
			}, "\n"),
			want: []string{`"delta":"ok"`, "event: response.completed"},
			deny: []string{"not-json"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := OpenAISSEToResponses(strings.NewReader(tc.in), &out, "m"); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("missing %s in %s", w, got)
				}
			}
			for _, d := range tc.deny {
				if strings.Contains(got, d) {
					t.Fatalf("must not contain %s: %s", d, got)
				}
			}
		})
	}
}

func TestOpenAISSEToResponsesEmptyWritesNothing(t *testing.T) {
	var out bytes.Buffer
	if err := OpenAISSEToResponses(strings.NewReader(""), &out, "m"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("empty upstream must not invent a Responses turn: %s", out.Bytes())
	}
}

func TestOpenAISSEToClaudeFixtures(t *testing.T) {
	in := strings.NewReader("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	var out bytes.Buffer
	if err := OpenAISSEToClaude(in, &out, "m"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, ev := range []string{"event: message_start", "event: content_block_delta", `"text":"hi"`, "event: message_stop"} {
		if !strings.Contains(got, ev) {
			t.Fatalf("missing %s in %s", ev, got)
		}
	}
	if strings.Contains(got, "event: message\n") {
		t.Fatal("must not wrap as a single event: message")
	}
}
