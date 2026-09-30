package clients

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #61: the UI used to hardcode the list of clients it would connect, so Pi was
// missing from it while `connect pi` worked from the CLI and the admin API.
// The list is now whatever Layout can actually write.
func TestConnectable(t *testing.T) {
	for _, name := range []string{"opencode", "continue", "codex", "claude-code", "pi"} {
		if !Connectable(name) {
			t.Errorf("%s is connectable from the CLI and the admin API but Connectable says otherwise", name)
		}
	}
	for _, name := range []string{"cursor", "cline", "amp", "droid", "", "not-a-client"} {
		if Connectable(name) {
			t.Errorf("%s has no managed config; Connectable should say so", name)
		}
	}
}

// #61 related: Pi reads its provider list from PI_CODING_AGENT_DIR when that is
// set, so connect has to write there rather than at <root>/.pi/agent.
func TestPiHonoursPiCodingAgentDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	l := Layout{Root: t.TempDir()}
	if got := l.path("pi"); got != filepath.Join(dir, "models.json") {
		t.Fatalf("path = %q, want %q", got, filepath.Join(dir, "models.json"))
	}
	if err := l.Connect("pi", DefaultOrigin+"/v1", ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatalf("connect did not write where pi looks: %v", err)
	}
	var got struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Providers["anthropic"].BaseURL != DefaultOrigin {
		t.Fatalf("anthropic baseUrl = %q, want no /v1: %s", got.Providers["anthropic"].BaseURL, raw)
	}
	if got.Providers["openai"].BaseURL != DefaultOrigin+"/v1" {
		t.Fatalf("openai baseUrl = %q, want the /v1: %s", got.Providers["openai"].BaseURL, raw)
	}
}

// Without the variable the layout root is the whole story, and an empty one
// must not send the write to the current directory.
func TestPiPathDefaultsToTheLayoutRoot(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	for _, root := range []string{t.TempDir(), ""} {
		l := Layout{Root: root}
		if got := l.path("pi"); got != filepath.Join(root, ".pi", "agent", "models.json") {
			t.Fatalf("root %q: path = %q", root, got)
		}
	}
}

// A path that is a file, not a directory, is not a place to put models.json.
func TestPiPathIgnoresANonDirectory(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", blocker)
	l := Layout{Root: root}
	if err := l.Connect("pi", DefaultOrigin, ""); err == nil {
		t.Fatal("expected an error when PI_CODING_AGENT_DIR is not a directory")
	}
	if _, err := os.Stat(blocker); err != nil {
		t.Fatalf("the file that stood in the way was disturbed: %v", err)
	}
}

// The env var is pi's alone; it must not move anyone else's config.
func TestPiCodingAgentDirDoesNotAffectOtherClients(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	root := t.TempDir()
	l := Layout{Root: root}
	for _, name := range []string{"claude-code", "codex", "continue", "opencode"} {
		if got := l.path(name); !strings.HasPrefix(got, root) {
			t.Errorf("%s moved to %q", name, got)
		}
	}
}
