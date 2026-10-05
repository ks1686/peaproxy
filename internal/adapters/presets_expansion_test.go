package adapters

import (
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/hosted"
)

// Every named provider speaks OpenAI chat completions over HTTPS and needs its
// own key. Two accounts sharing a base URL or an env var would be one account
// wearing two names, so both are checked.
func TestNewKeyPresetsAreDistinctAndComplete(t *testing.T) {
	presets := map[string]AccountPreset{}
	for _, p := range AccountPresets() {
		presets[p.ID] = p
	}

	want := []string{
		"alibaba-coding-plan", "deepseek-key", "mistral-key", "zai-key",
		"minimax-key", "together-key", "fireworks-key", "cohere-key",
	}
	for _, id := range want {
		p, ok := presets[id]
		if !ok {
			t.Errorf("preset %q is missing", id)
			continue
		}
		if !strings.HasPrefix(p.BaseURL, "https://") {
			t.Errorf("%s base URL is not https: %q", id, p.BaseURL)
		}
		if strings.Contains(p.BaseURL, "YOUR_") {
			t.Errorf("%s still has a placeholder in its base URL: %q", id, p.BaseURL)
		}
		if p.EnvKey == "" {
			t.Errorf("%s has no env key name", id)
		}
		if p.Adapter == "" {
			t.Errorf("%s has no adapter", id)
		}
		if p.Tier != "paid" {
			t.Errorf("%s tier = %q, want paid: these are metered providers, not free tiers", id, p.Tier)
		}
	}

	urls := map[string]string{}
	keys := map[string]string{}
	for _, id := range want {
		p, ok := presets[id]
		if !ok {
			continue
		}
		if other, dup := urls[p.BaseURL]; dup {
			t.Errorf("%s and %s share base URL %q", id, other, p.BaseURL)
		}
		urls[p.BaseURL] = id
		if other, dup := keys[p.EnvKey]; dup {
			t.Errorf("%s and %s share env key %q", id, other, p.EnvKey)
		}
		keys[p.EnvKey] = id
	}
}

// A preset PeaProxy has not exercised against the live API says so. Shipping a
// provider as working when nobody has called it is exactly the kind of claim
// the catalog otherwise refuses to make.
func TestNewPresetsAreNotClaimedVerified(t *testing.T) {
	for _, p := range AccountPresets() {
		switch p.ID {
		case "deepseek-key", "mistral-key", "zai-key", "minimax-key",
			"together-key", "fireworks-key", "cohere-key", "alibaba-coding-plan":
			if !p.Unverified {
				t.Errorf("%s must be marked unverified until PeaProxy calls it live", p.ID)
			}
			if !strings.Contains(strings.ToLower(p.Note), "not") {
				t.Errorf("%s note should say it is not verified: %q", p.ID, p.Note)
			}
		}
	}
}

// A preset that cannot open its adapter is a dead dropdown entry: the account
// looks addable and fails at use. Each new adapter must actually construct.
func TestNewPresetsOpenTheirAdapter(t *testing.T) {
	reg := DefaultRegistry()
	for _, id := range []string{
		"deepseek-key", "mistral-key", "zai-key", "minimax-key",
		"together-key", "fireworks-key", "cohere-key", "alibaba-coding-plan",
	} {
		p, ok := LookupPreset(id)
		if !ok {
			t.Errorf("preset %q is missing", id)
			continue
		}
		if _, err := reg.Open(p.Adapter, adapter.Options{BaseURL: p.BaseURL, APIKey: "test-key"}); err != nil {
			t.Errorf("preset %q adapter %q does not open: %v", id, p.Adapter, err)
		}
		if spec, ok := hosted.Lookup(p.Adapter); ok && spec.DefaultBaseURL != p.BaseURL {
			t.Errorf("preset %q base URL %q disagrees with spec %q", id, p.BaseURL, spec.DefaultBaseURL)
		}
	}
}
