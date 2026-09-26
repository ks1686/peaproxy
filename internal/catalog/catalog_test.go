package catalog

import (
	"testing"
)

func sampleModels() []Model {
	return []Model{
		{ID: "llama3.2", Provider: "ollama", AccountID: "ollama-local", Tier: TierLocal, Modalities: []string{"text"}, Status: "ready"},
		{ID: "openrouter/free-model", Provider: "openrouter", AccountID: "or-1", Tier: TierFree, Modalities: []string{"text"}, Status: "ready"},
		{ID: "groq/llama", Provider: "groq", AccountID: "groq-1", Tier: TierFreemium, Modalities: []string{"text"}, Status: "ready"},
		{ID: "claude-opus", Provider: "anthropic", AccountID: "anth-oauth", Tier: TierPaid, Modalities: []string{"text", "image_in"}, Status: "ready", SubscriptionOAuth: true},
		{ID: "gpt-4o", Provider: "openai", AccountID: "oa-key", Tier: TierPaid, Modalities: []string{"text", "image_in"}, Status: "ready"},
	}
}

func TestFilterAllKeepsEveryModel(t *testing.T) {
	got := List(sampleModels(), Query{Filter: FilterAll})
	if len(got) != 5 {
		t.Fatalf("FilterAll: got %d models, want 5", len(got))
	}
}

func TestFilterFreeIncludesFreeAndFreemium(t *testing.T) {
	got := List(sampleModels(), Query{Filter: FilterFree})
	if len(got) != 2 {
		t.Fatalf("FilterFree: got %d models, want 2 (free+freemium)", len(got))
	}
	for _, m := range got {
		if m.Tier != TierFree && m.Tier != TierFreemium {
			t.Fatalf("FilterFree included tier %q model %q", m.Tier, m.ID)
		}
	}
}

func TestFilterPaidExcludesLocalAndFree(t *testing.T) {
	got := List(sampleModels(), Query{Filter: FilterPaid})
	if len(got) != 2 {
		t.Fatalf("FilterPaid: got %d models, want 2", len(got))
	}
	for _, m := range got {
		if m.Tier != TierPaid {
			t.Fatalf("FilterPaid included tier %q model %q", m.Tier, m.ID)
		}
	}
}

func TestFilterLocal(t *testing.T) {
	got := List(sampleModels(), Query{Filter: FilterLocal})
	if len(got) != 1 || got[0].ID != "llama3.2" {
		t.Fatalf("FilterLocal: %#v", got)
	}
}

func TestFilterSubscriptionOAuth(t *testing.T) {
	got := List(sampleModels(), Query{Filter: FilterSubscriptionOAuth})
	if len(got) != 1 || got[0].ID != "claude-opus" {
		t.Fatalf("FilterSubscriptionOAuth: %#v", got)
	}
}

func TestHideProviderDropsAllModelsFromProviderListing(t *testing.T) {
	got := List(sampleModels(), Query{
		Filter:        FilterAll,
		HideProviders: []string{"anthropic"},
	})
	for _, m := range got {
		if m.Provider == "anthropic" {
			t.Fatalf("hidden provider still listed: %q", m.ID)
		}
	}
	if len(got) != 4 {
		t.Fatalf("got %d models after hiding anthropic, want 4", len(got))
	}
}

func TestHideDoesNotBlockRouting(t *testing.T) {
	q := Query{HideProviders: []string{"anthropic"}, HideModels: []string{"gpt-4o"}}
	m, ok := FindRoutable(sampleModels(), q, "gpt-4o")
	if !ok || m.AccountID != "oa-key" {
		t.Fatalf("hidden model must still route: %#v ok=%v", m, ok)
	}
	m, ok = FindRoutable(sampleModels(), q, "claude-opus")
	if !ok || m.AccountID != "anth-oauth" {
		t.Fatalf("hidden provider must still route: %#v ok=%v", m, ok)
	}
}

func TestBlockRoutingHonorsOptIn(t *testing.T) {
	q := Query{HideModels: []string{"gpt-4o"}, BlockRouting: true}
	if _, ok := FindRoutable(sampleModels(), q, "gpt-4o"); ok {
		t.Fatal("blockRouting should refuse hidden models")
	}
}

func TestHideModelDropsMatchingIDsFromList(t *testing.T) {
	got := List(sampleModels(), Query{
		Filter:     FilterAll,
		HideModels: []string{"gpt-4o"},
	})
	for _, m := range got {
		if m.ID == "gpt-4o" {
			t.Fatal("hidden model gpt-4o still listed")
		}
	}
}

func TestExposeSubsetIsWhatClientsSee(t *testing.T) {
	got := List(sampleModels(), Query{
		Filter:       FilterAll,
		ExposeModels: []string{"llama3.2", "claude-opus"},
		ForClients:   true,
	})
	if len(got) != 2 {
		t.Fatalf("expose subset: got %d, want 2", len(got))
	}
	if _, ok := FindRoutable(sampleModels(), Query{ExposeModels: []string{"llama3.2"}, ForClients: true}, "claude-opus"); !ok {
		t.Fatal("unexposed model must still be routable by id")
	}
}

func TestExposeEmptyMeansAllNonHidden(t *testing.T) {
	got := List(sampleModels(), Query{
		Filter:     FilterAll,
		ForClients: true,
	})
	if len(got) != 5 {
		t.Fatalf("empty expose list should pass all non-hidden; got %d", len(got))
	}
}

func TestOpenAIModelsShapeUsesIDOnly(t *testing.T) {
	exposed := List(sampleModels(), Query{Filter: FilterLocal, ForClients: true})
	out := ToOpenAIList(exposed)
	if out.Object != "list" {
		t.Fatalf("object: %q", out.Object)
	}
	if len(out.Data) != 1 || out.Data[0].ID != "llama3.2" {
		t.Fatalf("openai list: %#v", out.Data)
	}
	if out.Data[0].OwnedBy != "peaproxy" {
		t.Fatalf("owned_by should be peaproxy, not upstream provider; got %q", out.Data[0].OwnedBy)
	}
}
