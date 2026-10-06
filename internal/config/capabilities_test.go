package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// writeConfig drops YAML into a temp dir and loads it through the real path a
// user's config takes: Default() merged, decoded, secrets hydrated, Validate().
//
// The first draft of these tests called yaml.Unmarshal directly and called the
// helper "strict", which made them read like they proved the production loader
// accepts the field. They proved nothing of the kind. Parsing a struct is not
// loading a config.
func writeConfig(t *testing.T, body string) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "peaproxy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	return cfg
}

// An OpenAI-compatible endpoint is a shape, not a promise. Plenty of servers
// speak the shape and quietly ignore `tools`, and PeaProxy cannot tell by
// looking -- so the user has to be able to say what their endpoint does, or
// every declaration here stays an assertion the user cannot correct.
func TestProviderCapabilitiesSurviveTheRealLoader(t *testing.T) {
	cfg := writeConfig(t, `
schemaVersion: 1
providers:
  - id: local-llama
    adapter: openai_compat
    baseURL: http://127.0.0.1:11434/v1
    capabilities:
      tools: false
      embeddings: false
`)
	p := cfg.Providers[0]
	if p.Capabilities.Tools == nil || *p.Capabilities.Tools {
		t.Fatalf("tools = %v, want the configured false to survive loading", p.Capabilities.Tools)
	}
	if p.Capabilities.Embeddings == nil || *p.Capabilities.Embeddings {
		t.Fatalf("embeddings = %v, want the configured false", p.Capabilities.Embeddings)
	}
	// An unset capability stays unset, so "not configured" and "configured as
	// the default" remain different things. A default applied here would make
	// "the user said false" and "nobody said anything" the same object.
	if p.Capabilities.VisionIn != nil {
		t.Fatalf("visionIn = %v, want nil for a capability nobody configured", p.Capabilities.VisionIn)
	}
}

// An explicit true must be as loadable as an explicit false, or the field would
// only be usable to switch things off.
func TestProviderCapabilitiesAcceptAnExplicitTrue(t *testing.T) {
	cfg := writeConfig(t, `
schemaVersion: 1
providers:
  - id: p
    adapter: openai_compat
    baseURL: http://127.0.0.1:8000/v1
    capabilities:
      tools: true
      imageOut: true
`)
	p := cfg.Providers[0]
	if p.Capabilities.Tools == nil || !*p.Capabilities.Tools {
		t.Fatalf("tools = %v, want true", p.Capabilities.Tools)
	}
	if p.Capabilities.ImageOut == nil || !*p.Capabilities.ImageOut {
		t.Fatalf("imageOut = %v, want true", p.Capabilities.ImageOut)
	}
}

// A config without the block keeps working. This is additive, and an upgrade
// that refused existing configs would be a rollback nobody asked for.
func TestProviderCapabilitiesAreOptional(t *testing.T) {
	cfg := writeConfig(t, `
schemaVersion: 1
providers:
  - id: p
    adapter: openai_compat
    baseURL: http://127.0.0.1:8000/v1
`)
	if cfg.Providers[0].Capabilities.Tools != nil {
		t.Fatalf("an absent block produced a capability value: %v", *cfg.Providers[0].Capabilities.Tools)
	}
}

// A capability correction has to survive the round trip to disk. A setting that
// silently reverts on the next save is worse than one that was never accepted --
// the user would believe their endpoint was declared tool-less and it would not
// be.
func TestProviderCapabilitiesSurviveARoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	in := writeConfig(t, `
schemaVersion: 1
providers:
  - id: p
    adapter: openai_compat
    baseURL: http://127.0.0.1:8000/v1
    capabilities:
      tools: false
`)
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := out.Providers[0].Capabilities.Tools
	if got == nil || *got {
		t.Fatalf("tools = %v after Save and Load, want false", got)
	}
}

// The YAML keys are the user's interface, so the field tags are worth pinning.
// A rename that silently stopped reading existing configs would leave every
// user's declaration inert while the config still validated.
func TestProviderCapabilitiesYAMLKeysAreStable(t *testing.T) {
	b, err := yaml.Marshal(ProviderCapabilities{
		Tools:      boolPtr(false),
		VisionIn:   boolPtr(true),
		ImageOut:   boolPtr(false),
		Embeddings: boolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]bool
	if err := yaml.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"tools": false, "visionIn": true, "imageOut": false, "embeddings": true}
	if len(out) != len(want) {
		t.Fatalf("keys = %v, want %v", out, want)
	}
	for k, v := range want {
		got, ok := out[k]
		if !ok || got != v {
			t.Fatalf("key %q = %v (present=%v), want %v", k, got, ok, v)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
