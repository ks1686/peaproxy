package catalog

import "testing"

func TestApplyRoutesListsAliasAndDropsMissingTarget(t *testing.T) {
	all := sampleModels()
	all = append(all, Model{ID: "code", Provider: "openai", AccountID: "oa", Tier: TierPaid, Exposed: true, Routable: true})
	listed := List(all, Query{Filter: FilterAll})
	got := ApplyRoutes(listed, all, Query{}, map[string]string{
		"code": "llama3.2",
		"gone": "not-a-model",
	})
	var alias Model
	var liveCode int
	for _, m := range got {
		if m.ID == "gone" {
			t.Fatal("missing target was listed")
		}
		if m.ID == "code" {
			liveCode++
			alias = m
		}
	}
	if liveCode != 1 {
		t.Fatalf("code rows = %d in %#v", liveCode, ids(got))
	}
	if alias.AliasOf != "llama3.2" || alias.Provider == "openai" {
		t.Fatalf("alias %#v", alias)
	}
	if alias.AccountID == "" || len(alias.Modalities) == 0 {
		t.Fatalf("alias did not copy the live row: %#v", alias)
	}
}

func TestApplyRoutesHidesAliasWhenExposeListOmitsIt(t *testing.T) {
	all := sampleModels()
	q := Query{Filter: FilterAll, ForClients: true, ExposeModels: []string{"llama3.2"}}
	listed := List(all, q)
	got := ApplyRoutes(listed, all, q, map[string]string{"code": "llama3.2"})
	for _, m := range got {
		if m.ID == "code" {
			t.Fatal("expose list omitted the route name")
		}
	}
}

func ids(models []Model) []string {
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.ID
	}
	return out
}
