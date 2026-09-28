package clients

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	updated := strings.Replace(string(raw), "{", "{\n  \"user\": true,", 1)
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
	if strings.Contains(string(got), "peaproxy") || !strings.Contains(string(got), "user") {
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
