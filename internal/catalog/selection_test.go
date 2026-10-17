package catalog

import (
	"slices"
	"testing"
)

func TestBuildAddsThePublishedContextCeiling(t *testing.T) {
	idx := Build([]Model{{
		ID: "gpt-5.6-sol", DisplayName: "GPT-5.6 Sol",
		ContextWindow: 272000, MaxContextWindow: 1050000,
		Reasoning: true, Efforts: []string{"low", "high"},
		Routable: true, Exposed: true,
	}})
	got := idx.Clients()
	if len(got) != 2 {
		t.Fatalf("len %d", len(got))
	}
	alias := got[1]
	if alias.ID != "gpt-5.6-sol@1050000" || alias.ContextWindow != 1050000 || alias.MaxContextWindow != 0 || alias.AliasOf != "gpt-5.6-sol" {
		t.Fatalf("%+v", alias)
	}
	if alias.DisplayName != "GPT-5.6 Sol @1050000" || !slices.Equal(alias.Efforts, []string{"low", "high"}) {
		t.Fatalf("alias lost the model's other facts: %+v", alias)
	}
	if again := Build(got).Clients(); len(again) != 2 {
		t.Fatalf("second pass duplicated the ceiling: %d", len(again))
	}
	if got := idx.Target("gpt-5.6-sol@1050000", "low"); got != "gpt-5.6-sol" {
		t.Fatalf("ceiling target %s", got)
	}
}

func TestBuildCeilingIsTheMaxAcrossAccounts(t *testing.T) {
	rows := []Model{
		{ID: "claude-opus-5", ContextWindow: 1000000, Routable: true, Exposed: true},
		{ID: "claude-opus-5", ContextWindow: 200000, MaxContextWindow: 264000, Routable: true, Exposed: true},
		{ID: "gpt-5.6-sol", ContextWindow: 272000, MaxContextWindow: 400000, Routable: true, Exposed: true},
		{ID: "gpt-5.6-sol", ContextWindow: 272000, MaxContextWindow: 872000, Routable: true, Exposed: true},
	}
	for _, order := range [][]Model{rows, {rows[1], rows[0], rows[3], rows[2]}} {
		ids := idsOf(Build(order).Clients())
		if slices.Contains(ids, "claude-opus-5@264000") {
			t.Fatalf("smaller ceiling listed: %v", ids)
		}
		if !slices.Contains(ids, "gpt-5.6-sol@872000") || slices.Contains(ids, "gpt-5.6-sol@400000") {
			t.Fatalf("gpt ceilings: %v", ids)
		}
	}
}

func TestBuildEffortBaseFromLiveSiblings(t *testing.T) {
	idx := Build([]Model{
		{ID: "gemini-3.8-flash-high", ContextWindow: 1000, MaxContextWindow: 2000, Routable: true, Exposed: true},
		{ID: "gemini-3.8-flash-low", ContextWindow: 1000, MaxContextWindow: 2000, Routable: true, Exposed: true},
		{ID: "gemini-3.8-flash-medium", ContextWindow: 1000, MaxContextWindow: 2000, Routable: true, Exposed: true},
		{ID: "gemini-3.5-flash-extra-low", Routable: true, Exposed: true},
		{ID: "gemini-3.5-flash-lite", Routable: true, Exposed: true},
	})
	got := idx.Clients()
	var base Model
	var found bool
	for _, m := range got {
		if m.ID == "gemini-3.8-flash" {
			base = m
			found = true
		}
		if m.ID == "gemini-3.5-flash" || m.ID == "gemini-3.5-flash-extra" {
			t.Fatalf("a non-effort suffix became a base: %s", m.ID)
		}
	}
	if !found {
		t.Fatalf("base missing: %+v", idsOf(got))
	}
	if base.AliasOf != "gemini-3.8-flash-medium" || !base.Reasoning || base.ContextWindow != 1000 {
		t.Fatalf("base: %+v", base)
	}
	if !slices.Equal(base.Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("efforts %v", base.Efforts)
	}
	if got := idx.Target("gemini-3.8-flash", "high"); got != "gemini-3.8-flash-high" {
		t.Fatalf("effort sibling: %s", got)
	}
	if got := idx.Target("gemini-3.8-flash", ""); got != "gemini-3.8-flash-medium" {
		t.Fatalf("synthetic default: %s", got)
	}
	if got := idx.Target("gemini-3.8-flash@2000", "high"); got != "gemini-3.8-flash-high" {
		t.Fatalf("ceiling of a synthesized base: %s", got)
	}
	if got := idx.Target("gemini-3.8-flash@2000", ""); got != "gemini-3.8-flash-medium" {
		t.Fatalf("ceiling default: %s", got)
	}
	if got := idx.Target("gemini-3.8-flash-high", "low"); got != "gemini-3.8-flash-high" {
		t.Fatalf("an already-suffixed id was rewritten: %s", got)
	}
}

func TestBuildKeepsProviderEffortsOnAnExistingBase(t *testing.T) {
	idx := Build([]Model{
		{ID: "gemini-3-flash", Efforts: []string{"max"}, Reasoning: true, Routable: true},
		{ID: "gemini-3-flash-low", Routable: true},
		{ID: "gemini-3-flash-high", Routable: true},
	})
	got := idx.Clients()
	if len(got) != 3 {
		t.Fatalf("synthesized a second base: %+v", idsOf(got))
	}
	if !slices.Equal(got[0].Efforts, []string{"max"}) {
		t.Fatalf("provider efforts replaced: %v", got[0].Efforts)
	}
	if got := idx.Target("gemini-3-flash", "low"); got != "gemini-3-flash" {
		t.Fatalf("provider efforts were routed onto a sibling: %s", got)
	}
}

func TestBuildFillsAnExistingBaseThatPublishedNoEfforts(t *testing.T) {
	idx := Build([]Model{
		{ID: "gemini-3-flash", ContextWindow: 1000, Routable: true},
		{ID: "gemini-3-flash-low", Routable: true},
		{ID: "gemini-3-flash-high", Routable: true},
	})
	got := idx.Clients()
	if len(got) != 3 || !got[0].Reasoning || !slices.Equal(got[0].Efforts, []string{"low", "high"}) {
		t.Fatalf("%+v", got[0])
	}
	if live := idx.Target("gemini-3-flash", "low"); live != "gemini-3-flash-low" {
		t.Fatalf("family effort: %s", live)
	}
	if live := idx.Target("gemini-3-flash", ""); live != "gemini-3-flash" {
		t.Fatalf("live base with no effort: %s", live)
	}
}

func TestBuildLeavesAnUnknownAliasAlone(t *testing.T) {
	idx := Build([]Model{{ID: "gpt-5.6-sol", ContextWindow: 272000, MaxContextWindow: 1050000}})
	if got := idx.Target("gpt-5.6-sol@999", ""); got != "gpt-5.6-sol@999" {
		t.Fatalf("unrelated @ suffix was rewritten: %s", got)
	}
}

func idsOf(models []Model) []string {
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.ID
	}
	return out
}
