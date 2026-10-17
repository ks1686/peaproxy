package catalog

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

func TestTokensFromJSONAcceptsExactIntegersOnly(t *testing.T) {
	if got := TokensFromJSON([]byte(`1000000`)); got != 1000000 {
		t.Fatalf("integer: %d", got)
	}
	if got := TokensFromJSON([]byte(`"128000"`)); got != 128000 {
		t.Fatalf("quoted integer: %d", got)
	}
	for _, raw := range []string{``, `null`, `0`, `-1`, `1.5`, `"nope"`, `{}`} {
		if got := TokensFromJSON([]byte(raw)); got != 0 {
			t.Fatalf("%s invented %d", raw, got)
		}
	}
}

func TestAssignWindowsKeepsALargerCeilingDistinct(t *testing.T) {
	window, ceiling := AssignWindows(272000, 1050000)
	if window != 272000 || ceiling != 1050000 {
		t.Fatalf("window=%d ceiling=%d", window, ceiling)
	}
	window, ceiling = AssignWindows(0, 400000)
	if window != 400000 || ceiling != 0 {
		t.Fatalf("lone ceiling: window=%d ceiling=%d", window, ceiling)
	}
	window, ceiling = AssignWindows(128000, 128000)
	if window != 128000 || ceiling != 0 {
		t.Fatalf("equal sizes are not a choice: window=%d ceiling=%d", window, ceiling)
	}
}

func TestParseAnthropicListReadsPublishedEffortAndContext(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"claude-opus-5-5","display_name":"Claude Opus 5.5","max_input_tokens":1000000,"max_tokens":128000,
		 "capabilities":{"effort":{"supported":true,"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true},"xhigh":{"supported":true},"max":{"supported":true}},
		  "thinking":{"supported":true,"types":{"adaptive":{"supported":true},"disabled":{"supported":false},"enabled":{"supported":false}}},
		  "image_input":{"supported":true}}},
		{"id":"claude-haiku-5-5","display_name":"Claude Haiku 5.5","max_input_tokens":200000,"max_tokens":64000,
		 "capabilities":{"effort":{"supported":false,"low":{"supported":false},"medium":{"supported":false},"high":{"supported":false},"max":{"supported":false}},
		  "thinking":{"supported":false,"types":{"disabled":{"supported":true}}}}},
		{"id":"","display_name":"skip"},
		{"id":"claude-broken","max_input_tokens":"lots"}
	]}`)
	got, err := ParseAnthropicList(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len %d", len(got))
	}
	opus := got[0]
	if opus.ContextWindow != 1000000 || opus.MaxTokens != 128000 || opus.DisplayName != "Claude Opus 5.5" || !opus.Reasoning || !opus.HideEffortOff {
		t.Fatalf("opus: %+v", opus)
	}
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if !slices.Equal(opus.Efforts, want) {
		t.Fatalf("efforts %v", opus.Efforts)
	}
	if !slices.Contains(opus.Modalities, "image_in") {
		t.Fatalf("image input dropped: %v", opus.Modalities)
	}
	if got[1].Reasoning || len(got[1].Efforts) != 0 || got[1].ContextWindow != 200000 || got[1].HideEffortOff {
		t.Fatalf("haiku invented effort: %+v", got[1])
	}
	if got[2].ContextWindow != 0 || got[2].ID != "claude-broken" {
		t.Fatalf("bad window should not blank the row: %+v", got[2])
	}
}

func TestParseCodexCatalogReadsContextChoiceAndEffort(t *testing.T) {
	body := []byte(`{"models":[{
		"slug":"gpt-5.6-sol","display_name":"GPT-5.6 Sol",
		"context_window":272000,"max_context_window":1050000,
		"supported_reasoning_levels":[{"effort":"high"},{"effort":"low"},{"effort":"none"},{"effort":"xhigh"}],
		"input_modalities":["text","image"]
	}]}`)
	got, err := ParseCodexCatalog(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	m := got[0]
	if m.ID != "gpt-5.6-sol" || m.ContextWindow != 272000 || m.MaxContextWindow != 1050000 || !m.Reasoning {
		t.Fatalf("%+v", m)
	}
	if !slices.Equal(m.Efforts, []string{"none", "low", "high", "xhigh"}) {
		t.Fatalf("efforts %v", m.Efforts)
	}
	if !slices.Contains(m.Modalities, "image_in") {
		t.Fatalf("modalities %v", m.Modalities)
	}
}

func TestParseCodexCatalogFillsThinDataFromModels(t *testing.T) {
	body := []byte(`{
		"data":[{"id":"gpt-5.6-sol"}],
		"models":[{"slug":"gpt-5.6-sol","context_window":272000,"supported_reasoning_levels":[{"effort":"medium"}]}]
	}`)
	got, err := ParseCodexCatalog(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ContextWindow != 272000 || !slices.Equal(got[0].Efforts, []string{"medium"}) {
		t.Fatalf("%+v", got)
	}
}

func TestToOpenAIListPublishesFactsAndFillsFromALaterAccount(t *testing.T) {
	got := ToOpenAIList([]Model{
		{ID: "claude-opus-5-5"},
		{ID: "other"},
		{ID: "claude-opus-5-5", ContextWindow: 1000000, MaxTokens: 128000, Reasoning: true, Efforts: []string{"high", "max"}, HideEffortOff: true},
	})
	if len(got.Data) != 2 || got.Data[0].ID != "claude-opus-5-5" {
		t.Fatalf("%+v", got.Data)
	}
	row := got.Data[0]
	if row.OwnedBy != "peaproxy" || row.ContextWindow != 1000000 || row.MaxTokens != 128000 || !row.Reasoning || !row.HideEffortOff {
		t.Fatalf("%+v", row)
	}
	if !slices.Equal(row.Efforts, []string{"high", "max"}) {
		t.Fatalf("efforts %v", row.Efforts)
	}
	raw, err := json.Marshal(ToOpenAIList([]Model{{ID: "plain"}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"context_window", "efforts", "reasoning", "max_tokens"} {
		if bytes.Contains(raw, []byte(absent)) {
			t.Fatalf("unknown fact %s leaked: %s", absent, raw)
		}
	}
}
