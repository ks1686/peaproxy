package catalog

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestApplyRoutesListsAliasAndDropsMissingTarget(t *testing.T) {
	all := sampleModels()
	all = append(all, Model{ID: "code", Provider: "openai", AccountID: "oa", Tier: TierPaid, Exposed: true, Routable: true})
	listed := List(all, Query{Filter: FilterAll})
	got := ApplyRoutes(listed, all, Query{}, map[string]Route{
		"code": {Target: "llama3.2"},
		"gone": {Target: "not-a-model"},
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
	got := ApplyRoutes(listed, all, q, map[string]Route{"code": {Target: "llama3.2"}})
	for _, m := range got {
		if m.ID == "code" {
			t.Fatal("expose list omitted the route name")
		}
	}
}

// A pinned route is the client's way of naming one provider for a model that
// several accounts serve. The row has to say which account the name calls.
func TestApplyRoutesPinnedRouteNamesTheAccount(t *testing.T) {
	all := sampleModels()
	all = append(all,
		Model{ID: "gpt-4o", Provider: "openai", AccountID: "oa-key", Tier: TierPaid, Modalities: []string{"text"}, Status: "ready", Exposed: true, Routable: true},
		Model{ID: "gpt-4o", Provider: "copilot", AccountID: "ca-oauth", Tier: TierPaid, Modalities: []string{"text"}, Status: "ready", Exposed: true, Routable: true},
	)
	listed := List(all, Query{Filter: FilterAll})
	got := ApplyRoutes(listed, all, Query{}, map[string]Route{
		"copilot-gpt": {Target: "gpt-4o", Account: "ca-oauth"},
	})
	var alias Model
	for _, m := range got {
		if m.ID == "copilot-gpt" {
			alias = m
		}
	}
	if alias.AliasOf != "gpt-4o" {
		t.Fatalf("alias target = %q, want gpt-4o", alias.AliasOf)
	}
	if alias.AccountID != "ca-oauth" {
		t.Fatalf("pinned account = %q, want ca-oauth", alias.AccountID)
	}
}

func TestRouteYAMLRoundTrip(t *testing.T) {
	const doc = `
short: llama3.2
pinned:
  target: gpt-4o
  account: ca-oauth
`
	var got map[string]Route
	if err := yaml.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["short"].Target != "llama3.2" || got["short"].Account != "" {
		t.Fatalf("short form = %#v", got["short"])
	}
	if got["pinned"].Target != "gpt-4o" || got["pinned"].Account != "ca-oauth" {
		t.Fatalf("long form = %#v", got["pinned"])
	}
	out, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]Route
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-unmarshal: %v\n%s", err, out)
	}
	for name, want := range got {
		if back[name] != want {
			t.Fatalf("route %q = %#v, want %#v\n%s", name, back[name], want, out)
		}
	}
	// An unpinned route must not change shape on save.
	if strings.Contains(string(out), "target: llama3.2") {
		t.Fatalf("unpinned route was reshaped:\n%s", out)
	}
}

func ids(models []Model) []string {
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.ID
	}
	return out
}
