package clients

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectPreservesJSONKeyOrder(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "opencode.json")
	original := "{\"z\":1,\"theme\":\"dark\"}\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := Layout{Root: root}
	if err := layout.Connect("opencode", "http://127.0.0.1:8317/v1", "model"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Index(text, `"z"`) < 0 || strings.Index(text, `"z"`) > strings.Index(text, `"theme"`) {
		t.Fatalf("key order changed: %s", text)
	}
	if !strings.Contains(text, `"peaproxy"`) {
		t.Fatalf("owned key missing: %s", text)
	}
	if err := layout.Disconnect("opencode"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), `"peaproxy"`) || strings.Index(string(after), `"z"`) > strings.Index(string(after), `"theme"`) {
		t.Fatalf("disconnect rewrote user keys: %s", after)
	}
}

func TestPiConnectIsGuided(t *testing.T) {
	root := t.TempDir()
	err := (Layout{Root: root}).Connect("pi", "http://127.0.0.1:8317/v1", "model")
	if !errors.Is(err, ErrGuidedSetup) {
		t.Fatalf("connect pi: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("pi wrote %d files", len(entries))
	}
}

func TestManagedConnectPreservesComments(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "opencode.json")
	if err := os.WriteFile(path, []byte("// user note\n{\"theme\":\"dark\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := Layout{Root: root}
	if err := layout.Connect("opencode", "http://127.0.0.1:8317/v1", "model"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "// user note") || !strings.Contains(string(got), "dark") {
		t.Fatalf("comments or user keys lost: %s", got)
	}
	if !strings.Contains(string(got), "127.0.0.1:8317") {
		t.Fatalf("owned provider missing: %s", got)
	}
}

func TestDisconnectPreservesUserEdits(t *testing.T) {
	root := t.TempDir()
	layout := Layout{Root: root}
	if err := layout.Connect("continue", "http://127.0.0.1:8317/v1", "model"); err != nil {
		t.Fatal(err)
	}
	path := layout.path("continue")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(raw) + "name: Kept\n"
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := layout.Disconnect("continue"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "peaproxy-owned-start") || !strings.Contains(string(got), "name: Kept") {
		t.Fatalf("disconnect changed user data: %s", got)
	}
}

func TestConnectDetectsConcurrentEdit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "opencode.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := Layout{Root: root}
	beforeWrite = func(path string) error {
		return os.WriteFile(path, []byte("{\"other\":1}\n"), 0o600)
	}
	t.Cleanup(func() { beforeWrite = func(string) error { return nil } })
	if err := layout.Connect("opencode", "http://127.0.0.1:8317/v1", "m"); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestClientPathsPortable(t *testing.T) {
	root := t.TempDir()
	layout := Layout{Root: root}
	for _, name := range []string{"opencode", "codex", "claude-code"} {
		if err := layout.Connect(name, "http://127.0.0.1:8317/v1", "model"); err != nil {
			t.Fatal(name, err)
		}
		if _, err := os.Stat(layout.path(name)); err != nil {
			t.Fatal(name, err)
		}
	}
}

func TestContinueYAMLPreservesUnrelatedLines(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".continue", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# user note\nname: Mine\nmodels:\n  - name: Other\n    provider: openai\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := Layout{Root: root}
	if err := layout.Connect("continue", "http://127.0.0.1:8317/v1", "llama"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "# user note") || !strings.Contains(text, "name: Mine") || !strings.Contains(text, "name: Other") {
		t.Fatalf("unrelated yaml lost: %s", text)
	}
	if !strings.Contains(text, "apiBase: \"http://127.0.0.1:8317/v1\"") {
		t.Fatalf("owned model missing: %s", text)
	}
	if err := layout.Disconnect("continue"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "peaproxy-owned-start") || !strings.Contains(string(after), "name: Other") {
		t.Fatalf("disconnect changed user yaml: %s", after)
	}
}

func TestConnectIsIdempotent(t *testing.T) {
	root := t.TempDir()
	layout := Layout{Root: root}
	if err := layout.Connect("claude-code", "http://127.0.0.1:8317", "model"); err != nil {
		t.Fatal(err)
	}
	if err := layout.Connect("claude-code", "http://127.0.0.1:8317", "model"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(layout.path("claude-code"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), "peaproxy") != 1 {
		t.Fatalf("connect was not idempotent: %s", got)
	}
}
