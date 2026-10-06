package adapters

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

// A flat list of forty-odd entries is unusable in a dropdown, so every preset
// belongs to a group. No preset may be left ungrouped: the UI renders groups,
// and an ungrouped entry is one nobody can find.
func TestEveryPresetHasAGroup(t *testing.T) {
	known := map[string]bool{
		"Local models": true, "Official API keys": true,
		"Subscription OAuth": true, "Other": true,
	}
	for _, p := range AccountPresets() {
		if strings.TrimSpace(p.Group) == "" {
			t.Errorf("preset %q has no group", p.ID)
			continue
		}
		if !known[p.Group] {
			t.Errorf("preset %q uses unknown group %q", p.ID, p.Group)
		}
	}
}

// The groups arrive in a fixed order so the dropdown reads predictably: what is
// free and local first, then what costs money, then the subscription hacks
// users should think twice about.
func TestPresetGroupOrderIsStable(t *testing.T) {
	var order []string
	seen := map[string]bool{}
	for _, p := range AccountPresets() {
		if !seen[p.Group] {
			seen[p.Group] = true
			order = append(order, p.Group)
		}
	}
	want := []string{"Local models", "Official API keys", "Other", "Subscription OAuth"}
	if strings.Join(order, "|") != strings.Join(want, "|") {
		t.Fatalf("group order = %v, want %v", order, want)
	}
}

// Issue #115: the Qwen and Factory consumer OAuth flows do not exist. They are
// hidden from onboarding rather than offered and then failing.
func TestDeadOAuthStubsAreNotOffered(t *testing.T) {
	for _, p := range AccountPresets() {
		switch p.ID {
		case "qwen-oauth", "factory-oauth":
			t.Errorf("preset %q must not be offered for new accounts (#115)", p.ID)
		}
	}
	if _, ok := LookupPreset("qwen-oauth"); ok {
		t.Error("qwen-oauth is still offered")
	}
}

// Hiding them from the dropdown must not unregister the adapters. Someone who
// configured one before v3 keeps a working entry rather than a startup error.
func TestDeadOAuthAdaptersStayRegistered(t *testing.T) {
	reg := DefaultRegistry()
	for _, name := range []string{"qwen_oauth", "factory_oauth"} {
		if _, err := reg.Open(name, adapter.Options{APIKey: "unused"}); err != nil {
			t.Errorf("adapter %q stopped working: %v", name, err)
		}
	}
}

// The CLI lists preset ids for --help, so it must not advertise hidden ones.
func TestHiddenPresetsAreNotAdvertised(t *testing.T) {
	for _, id := range PresetIDs() {
		if id == "qwen-oauth" || id == "factory-oauth" {
			t.Errorf("hidden preset %q leaked into the advertised list", id)
		}
	}
}

// Group travels to the browser; the unverified flag must survive the hop too,
// or the UI cannot tell the user which entries have never been called.
func TestGroupAndVerificationTravelToTheClient(t *testing.T) {
	raw, err := json.Marshal(AccountPresets())
	if err != nil {
		t.Fatal(err)
	}
	var decoded []AccountPreset
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range decoded {
		if p.ID == "deepseek-key" {
			found = true
			if p.Group == "" {
				t.Error("group lost in JSON")
			}
			if !p.Unverified {
				t.Error("unverified flag lost in JSON")
			}
		}
	}
	if !found {
		t.Fatal("deepseek-key missing from the serialized payload")
	}
}
