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

func TestInferModalitiesVision(t *testing.T) {
	if got := InferModalities("llama3.2"); len(got) != 1 || got[0] != "text" {
		t.Fatalf("%v", got)
	}
	got := InferModalities("claude-sonnet-4-20250514")
	if len(got) != 2 || got[1] != "image_in" {
		t.Fatalf("%v", got)
	}
	got = InferModalities("gpt-4o-mini")
	if len(got) != 2 || got[1] != "image_in" {
		t.Fatalf("%v", got)
	}
}

func TestInferModalitiesImageOut(t *testing.T) {
	for _, id := range []string{"dall-e-3", "gpt-image-1", "black-forest-labs/flux-schnell"} {
		got := InferModalities(id)
		if !contains(got, "image_out") {
			t.Fatalf("%s: %v", id, got)
		}
		if contains(got, "image_in") {
			t.Fatalf("%s should not be tagged vision-in: %v", id, got)
		}
	}
}

func TestModalitiesFromLiveArchitecture(t *testing.T) {
	got := ModalitiesFromLive("openrouter/foo", []string{"text", "image"}, []string{"text", "image"})
	if !contains(got, "text") || !contains(got, "image_in") || !contains(got, "image_out") {
		t.Fatalf("%v", got)
	}
	// No live architecture → id inference.
	got = ModalitiesFromLive("dall-e-3", nil, nil)
	if !contains(got, "image_out") {
		t.Fatalf("%v", got)
	}
}

func TestInferModalitiesEmbeddings(t *testing.T) {
	for _, id := range []string{
		"text-embedding-3-small",
		"text-embedding-ada-002",
		"openai/text-embedding-3-large",
		"nomic-embed-text",
		"mxbai-embed-large",
		"gemini-embedding-001",
	} {
		got := InferModalities(id)
		if !contains(got, "embeddings") {
			t.Fatalf("%s: %v", id, got)
		}
	}
	if contains(InferModalities("llama3.2"), "embeddings") {
		t.Fatal("chat-only llama3.2 must not be tagged embeddings")
	}
	if contains(InferModalities("dall-e-3"), "embeddings") {
		t.Fatal("image-out models must not be tagged embeddings from id heuristics")
	}
	got := ModalitiesFromLive("vendor/vec", []string{"text"}, []string{"embeddings"})
	if !contains(got, "embeddings") {
		t.Fatalf("live embeddings output: %v", got)
	}
	got = ModalitiesFromLive("vendor/vec2", nil, []string{"embedding"})
	if !contains(got, "embeddings") {
		t.Fatalf("live embedding output: %v", got)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestAccountsForModelIncludesHidden(t *testing.T) {
	models := []Model{
		{ID: "gpt-4o", Provider: "openai", AccountID: "a", Tier: TierPaid},
		{ID: "gpt-4o", Provider: "openai", AccountID: "b", Tier: TierPaid},
	}
	q := Query{HideModels: []string{"gpt-4o"}}
	got := AccountsForModel(models, q, "gpt-4o")
	if len(got) != 2 {
		t.Fatalf("%v", got)
	}
	q.BlockRouting = true
	if got := AccountsForModel(models, q, "gpt-4o"); len(got) != 0 {
		t.Fatalf("blockRouting: %v", got)
	}
}

func TestRenameOverlaysDisplayNameWithoutChangingID(t *testing.T) {
	got := List(sampleModels(), Query{
		Filter: FilterAll,
		Rename: map[string]string{"llama3.2": "Llama 3.2 local"},
	})
	var llama Model
	for _, m := range got {
		if m.ID == "llama3.2" {
			llama = m
		}
	}
	if llama.DisplayName != "Llama 3.2 local" {
		t.Fatalf("displayName: %#v", llama)
	}
	if _, ok := FindRoutable(sampleModels(), Query{Rename: map[string]string{"llama3.2": "Llama 3.2 local"}}, "llama3.2"); !ok {
		t.Fatal("rename must not change routing id")
	}
	if _, ok := FindRoutable(sampleModels(), Query{Rename: map[string]string{"llama3.2": "Llama 3.2 local"}}, "Llama 3.2 local"); ok {
		t.Fatal("display name must not become a routing alias")
	}
}

func TestPinSortsPinnedModelsFirstInPinOrder(t *testing.T) {
	got := List(sampleModels(), Query{
		Filter: FilterAll,
		Pin:    []string{"gpt-4o", "llama3.2"},
	})
	if len(got) < 2 {
		t.Fatalf("%#v", got)
	}
	if got[0].ID != "gpt-4o" || !got[0].Pinned {
		t.Fatalf("first pinned: %#v", got[0])
	}
	if got[1].ID != "llama3.2" || !got[1].Pinned {
		t.Fatalf("second pinned: %#v", got[1])
	}
	for i := 2; i < len(got); i++ {
		if got[i].Pinned {
			t.Fatalf("unlisted pin leaked: %#v", got[i])
		}
	}
}

func TestPinAndRenameDoNotChangeHideRouting(t *testing.T) {
	q := Query{
		HideModels: []string{"gpt-4o"},
		Pin:        []string{"gpt-4o"},
		Rename:     map[string]string{"gpt-4o": "Hidden Omni"},
	}
	listed := List(sampleModels(), q)
	for _, m := range listed {
		if m.ID == "gpt-4o" {
			t.Fatal("pinned hidden model must stay off /v1/models")
		}
	}
	m, ok := FindRoutable(sampleModels(), q, "gpt-4o")
	if !ok || m.AccountID != "oa-key" {
		t.Fatalf("pin/rename must not block routing: %#v ok=%v", m, ok)
	}
	ann := AllAnnotated(sampleModels(), q)
	var hidden Model
	for _, row := range ann {
		if row.ID == "gpt-4o" {
			hidden = row
		}
	}
	if !hidden.Hidden || !hidden.Pinned || hidden.DisplayName != "Hidden Omni" || !hidden.Routable {
		t.Fatalf("annotated hidden pin: %#v", hidden)
	}
}
