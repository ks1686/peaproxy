package translate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestClaudeChatToolsRoundTripFixtures(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		deny []string
	}{
		{
			name: "claude tools become openai function tools",
			in: `{
				"model":"m",
				"max_tokens":16,
				"tools":[{"name":"lookup","description":"find","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}],
				"messages":[{"role":"user","content":"look"}]
			}`,
			want: []string{`"tools"`, `"type":"function"`, `"name":"lookup"`, `"find"`},
			deny: []string{`"input_schema"`},
		},
		{
			name: "assistant tool_use becomes tool_calls then adjacent tool results",
			in: `{
				"model":"m",
				"max_tokens":16,
				"messages":[
					{"role":"user","content":"look"},
					{"role":"assistant","content":[
						{"type":"text","text":"calling"},
						{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"x"}}
					]},
					{"role":"user","content":[
						{"type":"tool_result","tool_use_id":"toolu_1","content":"found"}
					]}
				]
			}`,
			want: []string{`"tool_calls"`, `"toolu_1"`, `"lookup"`, `"role":"tool"`, `"tool_call_id":"toolu_1"`, `"found"`, `"calling"`},
			deny: []string{`"tool_use"`, `"tool_result"`},
		},
		{
			name: "parallel tool_use stays one assistant then adjacent tool messages",
			in: `{
				"model":"m",
				"max_tokens":16,
				"messages":[
					{"role":"user","content":"both"},
					{"role":"assistant","content":[
						{"type":"tool_use","id":"a","name":"lookup","input":{"q":"1"}},
						{"type":"tool_use","id":"b","name":"ping","input":{}}
					]},
					{"role":"user","content":[
						{"type":"tool_result","tool_use_id":"a","content":"A"},
						{"type":"tool_result","tool_use_id":"b","content":[{"type":"text","text":"B"}]}
					]}
				]
			}`,
			want: []string{`"tool_calls"`, `"role":"tool"`, `"A"`, `"B"`},
			deny: []string{`"tool_use"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _, err := ToOpenAI([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
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

func TestToOpenAIToolUseAdjacency(t *testing.T) {
	in := []byte(`{
		"model":"m",
		"max_tokens":16,
		"messages":[
			{"role":"user","content":"look"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"x"}},
				{"type":"tool_use","id":"toolu_2","name":"ping","input":{}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"one"},
				{"type":"tool_result","tool_use_id":"toolu_2","content":"two"}
			]},
			{"role":"user","content":"thanks"}
		]
	}`)
	body, _, err := ToOpenAI(in)
	if err != nil {
		t.Fatal(err)
	}
	msgs := parseChatMessages(t, body)
	assertRoles(t, msgs, "user", "assistant", "tool", "tool", "user")
	assertToolAdjacent(t, msgs)
	if msgs[2].ToolCallID != "toolu_1" || msgs[3].ToolCallID != "toolu_2" {
		t.Fatalf("tool ids: %#v", msgs)
	}
	if !bytes.Contains(msgs[1].ToolCalls, []byte(`"toolu_1"`)) || !bytes.Contains(msgs[1].ToolCalls, []byte(`"toolu_2"`)) {
		t.Fatalf("batched tool_calls: %s", msgs[1].ToolCalls)
	}
}

func TestToClaudeToolRoleAdjacency(t *testing.T) {
	in := []byte(`{
		"model":"claude-sonnet-4-5",
		"max_tokens":16,
		"tools":[{"type":"function","function":{"name":"lookup","description":"find","parameters":{"type":"object"}}}],
		"messages":[
			{"role":"user","content":"look"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}},
				{"id":"call_2","type":"function","function":{"name":"ping","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_1","content":"A"},
			{"role":"tool","tool_call_id":"call_2","content":"B"},
			{"role":"user","content":"next"}
		]
	}`)
	out, err := ToClaude(in, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"name":"lookup"`)) || !bytes.Contains(out, []byte(`"input_schema"`)) {
		t.Fatalf("claude tools: %s", out)
	}
	var parsed struct {
		Tools []struct {
			Name     string          `json:"name"`
			Function json.RawMessage `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tools) != 1 || parsed.Tools[0].Name != "lookup" || len(parsed.Tools[0].Function) > 0 {
		t.Fatalf("tools must be Claude-shaped: %s", out)
	}
	msgs := parseClaudeMessages(t, out)
	if len(msgs) != 4 {
		t.Fatalf("want user, assistant, user(tool_results), user; got %d: %s", len(msgs), out)
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" || msgs[2].Role != "user" || msgs[3].Role != "user" {
		t.Fatalf("roles: %s", out)
	}
	asst := claudeBlockTypes(t, msgs[1].Content)
	if strings.Join(asst, ",") != "tool_use,tool_use" {
		t.Fatalf("assistant blocks %v: %s", asst, msgs[1].Content)
	}
	results := claudeBlockTypes(t, msgs[2].Content)
	if strings.Join(results, ",") != "tool_result,tool_result" {
		t.Fatalf("tool results must be one adjacent user turn, got %v: %s", results, msgs[2].Content)
	}
	if bytes.Contains(out, []byte(`"role":"tool"`)) {
		t.Fatalf("must not leak openai tool role: %s", out)
	}
}

func TestFromClaudeToolUseFinishReason(t *testing.T) {
	in := []byte(`{
		"id":"msg_1",
		"model":"c",
		"content":[
			{"type":"text","text":"calling"},
			{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"x"}}
		],
		"stop_reason":"tool_use"
	}`)
	out, err := FromClaude(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"finish_reason":"tool_calls"`)) {
		t.Fatalf("finish_reason: %s", out)
	}
	if !bytes.Contains(out, []byte(`"tool_calls"`)) || !bytes.Contains(out, []byte(`"toolu_1"`)) || !bytes.Contains(out, []byte(`"lookup"`)) {
		t.Fatalf("tool_calls: %s", out)
	}
	if !bytes.Contains(out, []byte(`"calling"`)) {
		t.Fatalf("text kept: %s", out)
	}
}

func TestFromOpenAIToolCallsBecomeToolUse(t *testing.T) {
	in := []byte(`{
		"id":"chatcmpl-1",
		"model":"m",
		"choices":[{
			"message":{
				"role":"assistant",
				"content":"calling",
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]
			},
			"finish_reason":"tool_calls"
		}]
	}`)
	out, err := FromOpenAI(in, "m")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"stop_reason":"tool_use"`)) {
		t.Fatalf("stop_reason: %s", out)
	}
	if !bytes.Contains(out, []byte(`"type":"tool_use"`)) || !bytes.Contains(out, []byte(`"call_1"`)) || !bytes.Contains(out, []byte(`"lookup"`)) {
		t.Fatalf("tool_use: %s", out)
	}
	if !bytes.Contains(out, []byte(`"calling"`)) {
		t.Fatalf("text kept: %s", out)
	}
}

func TestFromOpenAILengthDuringToolUse(t *testing.T) {
	in := []byte(`{
		"id":"chatcmpl-1",
		"model":"m",
		"choices":[{
			"message":{
				"role":"assistant",
				"content":null,
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"path\":\"a.txt\",\"body\":\"tru"}}]
			},
			"finish_reason":"length"
		}]
	}`)
	out, err := FromOpenAI(in, "m")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.StopReason != "max_tokens" {
		t.Fatalf("stop_reason %q, want max_tokens so the client does not run a truncated call: %s", got.StopReason, out)
	}
}

func TestRefusesUnknownNonTextClaudeBlock(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"thinking","thinking":"x"}]}]}`)
	_, _, err := ToOpenAI(in)
	if err == nil {
		t.Fatal("expected error for silent unknown-block drop")
	}
}
