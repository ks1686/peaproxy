package translate

import (
	"bytes"
	"testing"
)

func TestSplitThinkingSuffix(t *testing.T) {
	base, n := SplitThinkingSuffix("claude-sonnet-4-5-thinking-4000")
	if base != "claude-sonnet-4-5" || n != 4000 {
		t.Fatalf("%s %d", base, n)
	}
	base, n = SplitThinkingSuffix("code-thinking-10000")
	if base != "code" || n != 10000 {
		t.Fatalf("%s %d", base, n)
	}
	base, n = SplitThinkingSuffix("llama3.2")
	if base != "llama3.2" || n != 0 {
		t.Fatalf("%s %d", base, n)
	}
}

func TestApplyThinkingBudgetRaisesMaxTokens(t *testing.T) {
	in := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	out := ApplyThinkingBudget(in, 4000)
	if !bytes.Contains(out, []byte(`"budget_tokens":4000`)) || !bytes.Contains(out, []byte(`"type":"enabled"`)) {
		t.Fatalf("thinking missing: %s", out)
	}
	if !bytes.Contains(out, []byte(`"max_tokens":5024`)) {
		t.Fatalf("max_tokens: %s", out)
	}
	if !bytes.Contains(out, []byte(`"model":"claude-sonnet-4-5"`)) {
		t.Fatalf("model moved: %s", out)
	}
}

func TestApplyThinkingBudgetKeepsExistingThinking(t *testing.T) {
	in := []byte(`{"model":"c","max_tokens":8000,"thinking":{"type":"enabled","budget_tokens":1000},"messages":[]}`)
	out := ApplyThinkingBudget(in, 4000)
	if bytes.Contains(out, []byte(`"budget_tokens":4000`)) {
		t.Fatalf("replaced caller thinking: %s", out)
	}
	if !bytes.Contains(out, []byte(`"budget_tokens":1000`)) || !bytes.Contains(out, []byte(`"max_tokens":8000`)) {
		t.Fatalf("%s", out)
	}
}
