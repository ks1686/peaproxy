package promptcache

import (
	"bytes"
	"testing"
)

func TestCacheCountersProviderSemantics(t *testing.T) {
	got := NormalizeCounters(20, 5, 0, 100)
	if got.Read != 20 || got.Write != 5 || got.Uncached != 75 {
		t.Fatalf("%+v", got)
	}
	again := NormalizeCounters(20, 5, 75, 100)
	if again.Uncached != 75 {
		t.Fatalf("double count %+v", again)
	}
}

func TestPreserveCallerBreakpoints(t *testing.T) {
	in := []byte(`{"system":[{"cache_control":{"type":"ephemeral"},"text":"keep"}]}`)
	out, err := Apply(in, ModePreserve, "anthropic-claude")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(in) {
		t.Fatalf("preserve changed bytes: %s", out)
	}
}

func TestUnknownProfileAddsNoDirectives(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	out, err := Apply(in, ModeOptimize, "unknown-model")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(in) {
		t.Fatalf("unknown profile changed bytes: %s", out)
	}
}

func TestOptimizeHonorsBreakpointLimit(t *testing.T) {
	in := []byte(`{"a":"cache_control","b":"cache_control","c":"cache_control","d":"cache_control"}`)
	out, err := Apply(in, ModeOptimize, "anthropic-claude")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(in) {
		t.Fatalf("limit was not honored: %s", out)
	}
}

func TestOptimizeAddsBreakpointUnderLimit(t *testing.T) {
	in := []byte(`{"system":[{"type":"text","text":"keep"}],"messages":[{"role":"user","content":"hi"}]}`)
	out, err := Apply(in, ModeOptimize, "anthropic-claude")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"text":"keep"`)) || !bytes.Contains(out, []byte(`"cache_control"`)) {
		t.Fatalf("breakpoint missing: %s", out)
	}
	if !bytes.Contains(out, []byte(`"role":"user"`)) {
		t.Fatalf("messages reordered or dropped: %s", out)
	}
}

func TestOffLeavesCallerDirectives(t *testing.T) {
	in := []byte(`{"cache_control":{"type":"ephemeral"}}`)
	out, err := Apply(in, ModeOff, "anthropic-claude")
	if err != nil || string(out) != string(in) {
		t.Fatalf("out %s err %v", out, err)
	}
}
