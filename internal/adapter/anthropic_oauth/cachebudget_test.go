package anthropic_oauth

import (
	"encoding/json"
	"testing"
)

func countCacheControl(t *testing.T, body []byte) int {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	var walk func(any) int
	walk = func(v any) int {
		n := 0
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if k == "cache_control" {
					n++
					continue
				}
				n += walk(child)
			}
		case []any:
			for _, child := range x {
				n += walk(child)
			}
		}
		return n
	}
	return walk(v)
}

// The request omo (senpi) sends after one tool call: breakpoints on the system
// prompt, the tool list, the first user turn and the tool result, which is
// Anthropic's limit of 4. The cloak relocates the system prompt into a
// mid-conversation system turn and adds its own identity block; marking both
// pushed the upstream request to 5 and it answered HTTP 400.
const fourBreakpointRequest = `{"model":"claude-sonnet-5",
"system":[{"type":"text","text":"be terse","cache_control":{"type":"ephemeral"}}],
"tools":[{"name":"read","description":"Read a file. Mention cache_control here.","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],
"messages":[
 {"role":"user","content":[{"type":"text","text":"read it","cache_control":{"type":"ephemeral"}}]},
 {"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read","input":{"path":"a"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok","cache_control":{"type":"ephemeral"}}]}
]}`

func TestCloakKeepsCacheBreakpointsWithinAnthropicLimit(t *testing.T) {
	_, body, _ := captureMessages(t, "claude-sonnet-5", fourBreakpointRequest)
	if got := countCacheControl(t, body); got > maxCacheBreakpoints {
		t.Fatalf("upstream body carries %d cache_control breakpoints, Anthropic allows %d: %s", got, maxCacheBreakpoints, body)
	}
	msgs := decodeMessages(t, body)
	var relocated *capturedMessage
	for i := range msgs {
		if msgs[i].Role == "system" && messageText(msgs[i]) == "be terse" {
			relocated = &msgs[i]
		}
	}
	if relocated == nil {
		t.Fatalf("caller system not relocated: %s", body)
	}
	if countCacheControl(t, relocated.Content) != 1 {
		t.Fatalf("the relocated caller system keeps the one spare breakpoint: %s", relocated.Content)
	}
	sys := decodeSystemBlocks(t, body)
	if len(sys) != 2 || sys[1].Text != claudeCodeCLIIdentity {
		t.Fatalf("cloak system blocks changed: %#v", sys)
	}
	if sys[1].CacheControl != nil {
		t.Fatalf("identity block must give up its breakpoint when the budget is spent: %#v", sys[1].CacheControl)
	}
}

func TestCloakAddsNoBreakpointWhenCallerAlreadyUsesAllFour(t *testing.T) {
	raw := `{"model":"claude-sonnet-5",
"tools":[{"name":"a","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}},{"name":"b","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],
"messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}},{"type":"text","text":"y","cache_control":{"type":"ephemeral"}}]}]}`
	_, body, _ := captureMessages(t, "claude-sonnet-5", raw)
	if got := countCacheControl(t, body); got != maxCacheBreakpoints {
		t.Fatalf("want the caller's %d breakpoints and none added, got %d: %s", maxCacheBreakpoints, got, body)
	}
}

func TestCloakKeepsBothBreakpointsWhenBudgetAllows(t *testing.T) {
	raw := `{"model":"claude-sonnet-5","system":"be terse","messages":[{"role":"user","content":"hi"}]}`
	_, body, _ := captureMessages(t, "claude-sonnet-5", raw)
	sys := decodeSystemBlocks(t, body)
	if sys[1].CacheControl == nil {
		t.Fatalf("identity keeps its breakpoint when the budget allows: %#v", sys)
	}
	msgs := decodeMessages(t, body)
	if len(msgs) < 2 || msgs[1].Role != "system" || countCacheControl(t, msgs[1].Content) != 1 {
		t.Fatalf("relocated system keeps its breakpoint when the budget allows: %s", body)
	}
}
