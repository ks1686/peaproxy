package translate

import (
	"bytes"
	"encoding/json"
	"reflect"
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
			name: "text plus parallel tool_calls keep both and do not invent results",
			in: strings.Join([]string{
				`data: {"id":"c2","model":"m","choices":[{"delta":{"content":"calling"}}]}`,
				``,
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`,
				``,
				`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"ping","arguments":"{\"n\":1}"}}]}}]}`,
				``,
				`data: {"choices":[{"finish_reason":"tool_calls"}]}`,
				``,
				`data: [DONE]`,
				``,
				``,
			}, "\n"),
			want: []string{`"call_id":"call_a"`, `"call_id":"call_b"`, `"lookup"`, `"ping"`, `"output_text":"calling"`, "event: response.completed"},
			deny: []string{`"output":"executed"`},
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

func TestClaudeSSEToOpenAIFixtures(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   []string
		reason string
	}{
		{
			name: "text deltas end with finish_reason stop then [DONE]",
			in: strings.Join([]string{
				`event: message_start`,
				`data: {"type":"message_start","message":{"id":"msg","model":"c"}}`,
				``,
				`event: content_block_delta`,
				`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`,
				``,
				`event: message_delta`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
				``,
				``,
			}, "\n"),
			want:   []string{`"role":"assistant"`, `"content":"hi"`},
			reason: "stop",
		},
		{
			name: "tool_use stop_reason ends with finish_reason tool_calls then [DONE]",
			in: strings.Join([]string{
				`event: message_start`,
				`data: {"type":"message_start","message":{"id":"msg","model":"c"}}`,
				``,
				`event: content_block_start`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup"}}`,
				``,
				`event: content_block_delta`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}`,
				``,
				`event: message_delta`,
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
				``,
				``,
			}, "\n"),
			want:   []string{`"role":"assistant"`, `"tool_calls"`, `"toolu_1"`, `"lookup"`, `\"q\":\"x\"`},
			reason: "tool_calls",
		},
		{
			name: "text then tool_use still finish_reason tool_calls",
			in: strings.Join([]string{
				`event: message_start`,
				`data: {"type":"message_start","message":{"id":"msg2","model":"c"}}`,
				``,
				`event: content_block_delta`,
				`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}`,
				``,
				`event: content_block_start`,
				`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"ping"}}`,
				``,
				`event: message_delta`,
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
				``,
				``,
			}, "\n"),
			want:   []string{`"content":"ok"`, `"tool_calls"`, `"toolu_2"`, `"ping"`},
			reason: "tool_calls",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := ClaudeSSEToOpenAI(strings.NewReader(tc.in), &out); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("missing %s in %s", w, got)
				}
			}
			assertChatSSEFinish(t, got, tc.reason)
		})
	}
}

func TestOpenAISSEToClaudeFixtures(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		deny []string
	}{
		{
			name: "text deltas are true Anthropic events",
			in:   "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
			want: []string{"event: message_start", "event: content_block_delta", `"text":"hi"`, "event: message_stop"},
			deny: []string{"event: message\n"},
		},
		{
			name: "message_start and message_delta carry the usage the Anthropic SDK requires",
			in:   "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
			want: []string{`"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`, `"stop_sequence":null},"usage":{"output_tokens":0}}`},
		},
		{
			name: "chat tool_calls stream as Claude tool_use blocks",
			in: strings.Join([]string{
				`data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}]}}]}`,
				``,
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}`,
				``,
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"peas\"}"}}]}}]}`,
				``,
				`data: {"choices":[{"finish_reason":"tool_calls"}]}`,
				``,
				`data: [DONE]`,
				``,
				``,
			}, "\n"),
			want: []string{
				`"index":1,"content_block":{"type":"tool_use","id":"call_1","name":"lookup","input":{}}`,
				`"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"peas\"}"}`,
				`"stop_reason":"tool_use"`,
				"event: message_stop",
			},
			deny: []string{`"output":"executed"`, `"stop_reason":"end_turn"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := OpenAISSEToClaude(strings.NewReader(tc.in), &out, "m"); err != nil {
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

func TestOpenAISSEToClaudeBlocksNeverOverlap(t *testing.T) {
	in := strings.Join([]string{
		`data: {"id":"c1","model":"m","choices":[{"delta":{"content":"looking"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"one","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"two","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	var out strings.Builder
	if err := OpenAISSEToClaude(strings.NewReader(in), &out, "m"); err != nil {
		t.Fatal(err)
	}
	replayClaudeBlocks(t, out.String())
	for _, want := range []string{`"name":"one"`, `"name":"two"`, `"index":2`, `"stop_reason":"tool_use"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s in %s", want, out.String())
		}
	}
}

func TestOpenAISSEToClaudeInterleavedToolArguments(t *testing.T) {
	in := strings.Join([]string{
		`data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"one","arguments":"{\"x\":"}},{"index":1,"id":"b","function":{"name":"two","arguments":"{\"y\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}},{"index":1,"function":{"arguments":"2}"}}]}}]}`,
		`data: {"choices":[{"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	var out strings.Builder
	if err := OpenAISSEToClaude(strings.NewReader(in), &out, "m"); err != nil {
		t.Fatal(err)
	}
	got := replayClaudeBlocks(t, out.String())
	want := []replayedTool{{name: "one", input: `{"x":1}`}, {name: "two", input: `{"y":2}`}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tool inputs %+v, want %+v:\n%s", got, want, out.String())
	}
}

func TestOpenAISSEToClaudeLengthDuringToolUse(t *testing.T) {
	in := strings.Join([]string{
		`data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"path\":\"a.txt\",\"body\":\"tru"}}]}}]}`,
		`data: {"choices":[{"finish_reason":"length"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	var out strings.Builder
	if err := OpenAISSEToClaude(strings.NewReader(in), &out, "m"); err != nil {
		t.Fatal(err)
	}
	var delta struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(singleClaudeEvent(t, out.String(), "message_delta"), &delta); err != nil {
		t.Fatal(err)
	}
	if delta.Delta.StopReason != "max_tokens" {
		t.Fatalf("stop_reason %q, want max_tokens so the client does not run a truncated call:\n%s", delta.Delta.StopReason, out.String())
	}
}

func TestOpenAISSEToClaudePreservesUsageChunk(t *testing.T) {
	in := strings.Join([]string{
		`data: {"id":"c1","model":"m","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":9}}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	var out strings.Builder
	if err := OpenAISSEToClaude(strings.NewReader(in), &out, "m"); err != nil {
		t.Fatal(err)
	}
	var delta struct {
		Usage map[string]int `json:"usage"`
	}
	if err := json.Unmarshal(singleClaudeEvent(t, out.String(), "message_delta"), &delta); err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"input_tokens": 120, "output_tokens": 9}; !reflect.DeepEqual(delta.Usage, want) {
		t.Fatalf("message_delta usage %v, want upstream %v:\n%s", delta.Usage, want, out.String())
	}
}

func singleClaudeEvent(t *testing.T, sse, typ string) []byte {
	t.Helper()
	var found []byte
	for _, payload := range sseDataPayloads(sse) {
		var ev struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != typ {
			continue
		}
		if found != nil {
			t.Fatalf("more than one %s event:\n%s", typ, sse)
		}
		found = []byte(payload)
	}
	if found == nil {
		t.Fatalf("no %s event:\n%s", typ, sse)
	}
	return found
}

type replayedTool struct {
	name, input string
}

// replayClaudeBlocks reads Anthropic SSE the way SDK consumers do: one block
// open at a time, deltas only for the open block, and a tool_use input final
// at its content_block_stop. It returns the finalized tool inputs in order.
func replayClaudeBlocks(t *testing.T, sse string) []replayedTool {
	t.Helper()
	open, tool := -1, ""
	var args strings.Builder
	var tools []replayedTool
	for _, payload := range sseDataPayloads(sse) {
		var ev struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatal(err)
		}
		switch ev.Type {
		case "content_block_start":
			if open >= 0 {
				t.Fatalf("block %d started while %d open:\n%s", ev.Index, open, sse)
			}
			open, tool = ev.Index, ""
			if ev.ContentBlock.Type == "tool_use" {
				tool = ev.ContentBlock.Name
			}
			args.Reset()
		case "content_block_delta":
			if ev.Index != open {
				t.Fatalf("delta for %d while %d open:\n%s", ev.Index, open, sse)
			}
			args.WriteString(ev.Delta.PartialJSON)
		case "content_block_stop":
			if ev.Index != open {
				t.Fatalf("stop for %d while %d open:\n%s", ev.Index, open, sse)
			}
			if tool != "" {
				tools = append(tools, replayedTool{name: tool, input: args.String()})
			}
			open = -1
		}
	}
	if open != -1 {
		t.Fatalf("block %d never closed:\n%s", open, sse)
	}
	return tools
}

func TestClaudeSSEToOpenAIEmptyWritesNothing(t *testing.T) {
	var out bytes.Buffer
	if err := ClaudeSSEToOpenAI(strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("empty upstream must not invent chat SSE: %s", out.Bytes())
	}
}
