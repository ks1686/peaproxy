package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #77 end to end: the reported symptom is that the account works until the
// process restarts, because the secret that was just written is deleted by the
// prune that follows it. A config with a slashed id must still hydrate after a
// save and reload.
func TestSlashIDSurvivesSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	cfg := Config{SchemaVersion: SchemaVersion, Bind: "127.0.0.1", Port: DefaultPort}
	cfg.Providers = []Provider{
		{ID: "work/openai", Adapter: "openai_key", BaseURL: "https://api.openai.com", APIKey: "sk-work"},
		{ID: "personal", Adapter: "openai_key", BaseURL: "https://api.openai.com", APIKey: "sk-personal"},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	// The key must not be sitting in the YAML.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-work") || strings.Contains(string(raw), "sk-personal") {
		t.Fatal("an api key was written to the config in plaintext")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]string{}
	for _, p := range loaded.Providers {
		byID[p.ID] = p.APIKey
	}
	for id, want := range map[string]string{"work/openai": "sk-work", "personal": "sk-personal"} {
		if got := byID[id]; got != want {
			t.Errorf("%s hydrated to %q, want %q -- the secret was pruned after it was written", id, got, want)
		}
	}
}

// Two slashed ids that share a prefix must not collide in the store.
func TestSlashIDsThatShareAPrefixDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	cfg := Config{SchemaVersion: SchemaVersion, Bind: "127.0.0.1", Port: DefaultPort}
	cfg.Providers = []Provider{
		{ID: "work/openai", Adapter: "openai_key", BaseURL: "https://api.openai.com", APIKey: "sk-a"},
		{ID: "work/openai-mini", Adapter: "openai_key", BaseURL: "https://api.openai.com", APIKey: "sk-b"},
		{ID: "work", Adapter: "openai_key", BaseURL: "https://api.openai.com", APIKey: "sk-c"},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"work/openai": "sk-a", "work/openai-mini": "sk-b", "work": "sk-c"}
	if len(loaded.Providers) != len(want) {
		t.Fatalf("loaded %d providers, want %d", len(loaded.Providers), len(want))
	}
	for _, p := range loaded.Providers {
		if got := p.APIKey; got != want[p.ID] {
			t.Errorf("%s hydrated to %q, want %q", p.ID, got, want[p.ID])
		}
	}
}
