package usage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestParsePublishedUsageReadsDocumentedFields(t *testing.T) {
	p, c, cost, ok := ParsePublishedUsage([]byte(`{"usage":{"prompt_tokens":3,"completion_tokens":5}}`))
	if !ok || p != 3 || c != 5 || cost != nil {
		t.Fatalf("openai p=%d c=%d cost=%v ok=%v", p, c, cost, ok)
	}
	p, c, cost, ok = ParsePublishedUsage([]byte(`{"usage":{"input_tokens":0,"output_tokens":2}}`))
	if !ok || p != 0 || c != 2 || cost != nil {
		t.Fatalf("explicit zero must count: p=%d c=%d ok=%v cost=%v", p, c, ok, cost)
	}
	p, c, cost, ok = ParsePublishedUsage([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":0.25}}`))
	if !ok || cost == nil || *cost != 0.25 {
		t.Fatalf("published cost %v ok=%v", cost, ok)
	}
	_, _, cost, ok = ParsePublishedUsage([]byte(`{"id":"x"}`))
	if ok || cost != nil {
		t.Fatal("missing usage must not invent tokens or a price")
	}
	_, _, _, ok = ParsePublishedUsage([]byte("data: {\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":6}}\n\n"))
	if !ok {
		t.Fatal("sse usage chunk")
	}
}

func TestDailyRollupSurvivesRingAndOmitsUnpricedCalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	s := Open(path)
	day := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	next := day.Add(24 * time.Hour)
	cost := 0.5
	s.Add(Event{Time: day, AccountID: "a", Provider: "openai", Status: 200, PromptTokens: 3, CompletionTokens: 5, TokensKnown: true, CostUSD: &cost})
	s.Add(Event{Time: day, AccountID: "a", Provider: "openai", Status: 500, Error: "boom", PromptTokens: 1, CompletionTokens: 1, TokensKnown: true})
	s.Add(Event{Time: next, AccountID: "b", Provider: "ollama", Status: 200})
	for i := 0; i < capEvents; i++ {
		s.Add(Event{Time: next, AccountID: "b", Provider: "ollama", Status: 200})
	}
	if len(s.Recent()) != capEvents {
		t.Fatalf("ring %d", len(s.Recent()))
	}
	again := Open(path)
	days := again.ByDay()
	if len(days) < 2 {
		t.Fatalf("days dropped with the ring: %#v", days)
	}
	var first, second DayRollup
	for _, d := range days {
		switch d.Day {
		case "2026-09-01":
			first = d
		case "2026-09-02":
			second = d
		}
	}
	if first.AccountID != "a" || first.Calls != 2 || first.Errors != 1 || first.PromptTokens != 4 || first.CompletionTokens != 6 {
		t.Fatalf("day one %#v", first)
	}
	if first.CostUSD == nil || *first.CostUSD != 0.5 || first.CostCalls != 1 {
		t.Fatalf("partial published cost %#v", first)
	}
	if second.AccountID != "b" || second.Calls < 1 || second.CostUSD != nil || second.PromptTokens != 0 {
		t.Fatalf("unpriced day must omit cost: %#v", second)
	}
}

func TestDailyRollupDropsDaysOlderThanRetention(t *testing.T) {
	s := Open("")
	old := time.Now().UTC().AddDate(0, 0, -120)
	fresh := time.Now().UTC()
	s.Add(Event{Time: old, AccountID: "old", Status: 200})
	s.Add(Event{Time: fresh, AccountID: "new", Status: 200})
	for _, d := range s.ByDay() {
		if d.AccountID == "old" {
			t.Fatalf("kept %s", d.Day)
		}
	}
}
