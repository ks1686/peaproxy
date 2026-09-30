package clients

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConnectPreservesJSONKeyOrder(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "{\"z\":1,\"theme\":\"dark\",\"env\":{\"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS\":\"1\"}}\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := Layout{Root: root}
	if err := layout.Connect("claude-code", "http://127.0.0.1:8317/v1", "claude-opus-5-5"); err != nil {
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
	var parsed struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("settings invalid: %v\n%s", err, got)
	}
	if parsed.Env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:8317" || parsed.Env["ANTHROPIC_API_KEY"] != "peaproxy" || parsed.Env["ANTHROPIC_MODEL"] != "claude-opus-5-5" {
		t.Fatalf("claude-code env must carry base url WITHOUT /v1, key and model: %s", text)
	}
	if parsed.Env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"] != "1" {
		t.Fatalf("user env lost: %s", text)
	}
	if strings.Contains(text, `"peaproxy":`) {
		t.Fatalf("no inert top-level key: %s", text)
	}
	if err := layout.Disconnect("claude-code"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "ANTHROPIC_") || !strings.Contains(string(after), "AGENT_TEAMS") || strings.Index(string(after), `"z"`) > strings.Index(string(after), `"theme"`) {
		t.Fatalf("disconnect rewrote user keys: %s", after)
	}
}

func TestClaudeCodeReconnectDropsModelAndTrimsV1Slash(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	layout := Layout{Root: root}
	readEnv := func() map[string]string {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal(got, &parsed); err != nil {
			t.Fatalf("settings invalid: %v\n%s", err, got)
		}
		return parsed.Env
	}
	if err := layout.Connect("claude-code", "http://127.0.0.1:8317/v1", "claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if err := layout.Connect("claude-code", "http://127.0.0.1:8317/v1/", ""); err != nil {
		t.Fatal(err)
	}
	env := readEnv()
	if _, ok := env["ANTHROPIC_MODEL"]; ok {
		t.Fatalf("empty model must delete ANTHROPIC_MODEL: %v", env)
	}
	if env["ANTHROPIC_API_KEY"] != "peaproxy" {
		t.Fatalf("api key lost on reconnect: %v", env)
	}
	if got := env["ANTHROPIC_BASE_URL"]; got != "http://127.0.0.1:8317" {
		t.Fatalf("base url = %q, want no /v1 and no trailing slash", got)
	}
}

func TestClaudeCodeDisconnectRemovesEmptyEnvAndLegacyKey(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"peaproxy":{"baseURL":"http://127.0.0.1:8317/v1","model":"m"},"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8317","ANTHROPIC_API_KEY":"peaproxy"},"theme":"dark"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Layout{Root: root}).Disconnect("claude-code"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"theme\":\"dark\"}\n"; string(after) != want {
		t.Fatalf("got %s want %s", after, want)
	}
}

func TestClaudeCodeDisconnectLeavesForeignKey(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://gateway.example","ANTHROPIC_API_KEY":"sk-ant-real"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Layout{Root: root}).Disconnect("claude-code"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "gateway.example") || !strings.Contains(string(after), "sk-ant-real") {
		t.Fatalf("disconnect must only touch an env whose ANTHROPIC_API_KEY is peaproxy: %s", after)
	}
}

func TestOpenCodeConnectIsGuided(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "opencode.json")
	if err := os.WriteFile(path, []byte("{\"theme\":\"dark\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := Layout{Root: root}
	if err := layout.Connect("opencode", "http://127.0.0.1:8317/v1", "m"); !errors.Is(err, ErrGuidedSetup) {
		t.Fatalf("connect opencode = %v, want guided: a custom provider needs per-model metadata", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "{\"theme\":\"dark\"}\n" {
		t.Fatalf("guided connect must not touch the file: %v %s", err, got)
	}
	if err := os.WriteFile(path, []byte(`{"peaproxy":{"baseURL":"x","model":"m"},"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := layout.Disconnect("opencode"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), `"peaproxy"`) || !strings.Contains(string(after), "dark") {
		t.Fatalf("disconnect must still drop the legacy inert key: %s", after)
	}
}

func TestPiConnectWritesModelsJSON(t *testing.T) {
	root := t.TempDir()
	layout := Layout{Root: root}
	if err := layout.Connect("pi", "http://127.0.0.1:8317/v1", ""); err != nil {
		t.Fatalf("connect pi: %v", err)
	}
	path := filepath.Join(root, ".pi", "agent", "models.json")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Providers map[string]map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("models.json invalid: %v\n%s", err, got)
	}
	if parsed.Providers["anthropic"]["baseUrl"] != "http://127.0.0.1:8317" || parsed.Providers["anthropic"]["apiKey"] != "peaproxy" {
		t.Fatalf("anthropic must be overridden without /v1: %s", got)
	}
	if parsed.Providers["openai"]["baseUrl"] != "http://127.0.0.1:8317/v1" || parsed.Providers["openai"]["apiKey"] != "peaproxy" {
		t.Fatalf("openai must be overridden with /v1: %s", got)
	}
	found := layout.Detect()
	if len(found) != 1 || found[0].Name != "pi" || found[0].Path != path {
		t.Fatalf("detect = %+v", found)
	}
	if err := layout.Disconnect("pi"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "peaproxy") || strings.Contains(string(after), "8317") {
		t.Fatalf("disconnect left owned fields: %s", after)
	}
}

func TestPiConnectMergesIntoUserProviders(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".pi", "agent", "models.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{
  "providers": {
    "ollama": {"baseUrl": "http://localhost:11434/v1", "api": "openai-completions", "apiKey": "ollama", "models": [{"id": "qwen2.5-coder:7b"}]},
    "anthropic": {"modelOverrides": {"claude-opus-5-5": {"promptCache": {"short": 300}}}}
  }
}
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := Layout{Root: root}
	for i := 0; i < 2; i++ {
		if err := layout.Connect("pi", "http://127.0.0.1:8317/v1", "ignored"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Count(text, `"apiKey": "peaproxy"`) != 2 {
		t.Fatalf("connect must be idempotent and own exactly anthropic+openai: %s", text)
	}
	if !strings.Contains(text, "qwen2.5-coder:7b") || !strings.Contains(text, `"modelOverrides"`) {
		t.Fatalf("user providers or fields lost: %s", text)
	}
	if strings.Index(text, `"ollama"`) > strings.Index(text, `"anthropic"`) {
		t.Fatalf("provider order changed: %s", text)
	}
	if strings.Contains(text, "ignored") {
		t.Fatalf("--model must not be recorded for pi (the whole catalog routes through): %s", text)
	}
	if err := layout.Disconnect("pi"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Providers map[string]map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(after, &parsed); err != nil {
		t.Fatalf("after disconnect invalid: %v\n%s", err, after)
	}
	if _, ok := parsed.Providers["openai"]; ok {
		t.Fatalf("an openai entry we created must be removed whole: %s", after)
	}
	anth := parsed.Providers["anthropic"]
	if _, ok := anth["modelOverrides"]; !ok || anth["baseUrl"] != nil || anth["apiKey"] != nil {
		t.Fatalf("disconnect must strip only baseUrl/apiKey from a user-owned anthropic entry: %s", after)
	}
	if parsed.Providers["ollama"]["apiKey"] != "ollama" {
		t.Fatalf("disconnect touched an unrelated provider: %s", after)
	}
}

func TestPiDisconnectLeavesForeignBaseURL(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".pi", "agent", "models.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{"providers":{"anthropic":{"baseUrl":"https://gateway.example/anthropic","apiKey":"$MY_KEY"}}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Layout{Root: root}).Disconnect("pi"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "gateway.example") || !strings.Contains(string(after), "$MY_KEY") {
		t.Fatalf("disconnect must only remove entries whose apiKey is peaproxy: %s", after)
	}
}

func TestManagedConnectPreservesComments(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("// user note\n{\"theme\":\"dark\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := Layout{Root: root}
	if err := layout.Connect("claude-code", "http://127.0.0.1:8317/v1", "model"); err != nil {
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
	path := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := Layout{Root: root}
	beforeWrite = func(path string) error {
		return os.WriteFile(path, []byte("{\"other\":1}\n"), 0o600)
	}
	t.Cleanup(func() { beforeWrite = func(string) error { return nil } })
	if err := layout.Connect("claude-code", "http://127.0.0.1:8317/v1", "m"); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestClientPathsPortable(t *testing.T) {
	root := t.TempDir()
	layout := Layout{Root: root}
	for _, name := range []string{"pi", "codex", "claude-code"} {
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

func TestQuoteEscapesBackslashAndNewline(t *testing.T) {
	for in, want := range map[string]string{
		`a\b`:  `"a\\b"`,
		`a"b`:  `"a\"b"`,
		"a\nb": `"a\nb"`,
		"a\tb": `"a\tb"`,
		"a<b":  `"a<b"`,
	} {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestCodexConnectRejectsInjectingModel(t *testing.T) {
	root := t.TempDir()
	layout := Layout{Root: root}
	if err := layout.Connect("codex", "http://127.0.0.1:8317/v1", "x\"\nmodel = \"evil"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("connect = %v, want ErrInvalidInput", err)
	}
	if _, err := os.Stat(layout.path("codex")); !os.IsNotExist(err) {
		t.Fatalf("rejected connect wrote config.toml: %v", err)
	}
	text := string(insertCodex(nil, "http://127.0.0.1:8317/v1", `m\"`+"\nmodel = \"evil"))
	if n := strings.Count(text, "\nmodel = "); n != 1 {
		t.Fatalf("model lines = %d, want 1:\n%s", n, text)
	}
}

func writeFixture(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestClaudeCodeConnectPreservesIndent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	original := "{\n  \"theme\": \"dark\",\n  \"env\": {\n    \"FOO\": \"1\"\n  }\n}\n"
	writeFixture(t, path, original)
	layout := Layout{Root: root}
	if err := layout.Connect("claude-code", "http://127.0.0.1:8317/v1", "m"); err != nil {
		t.Fatal(err)
	}
	expected := struct {
		Theme string `json:"theme"`
		Env   struct {
			FOO     string `json:"FOO"`
			BaseURL string `json:"ANTHROPIC_BASE_URL"`
			APIKey  string `json:"ANTHROPIC_API_KEY"`
			Model   string `json:"ANTHROPIC_MODEL"`
		} `json:"env"`
	}{Theme: "dark"}
	expected.Env.FOO = "1"
	expected.Env.BaseURL = "http://127.0.0.1:8317"
	expected.Env.APIKey = "peaproxy"
	expected.Env.Model = "m"
	want, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, string(want)+"\n")
	if err := layout.Disconnect("claude-code"); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, original)
}

func TestPiConnectPreservesIndent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".pi", "agent", "models.json")
	original := "{\n\t\"providers\": {\n\t\t\"ollama\": {\n\t\t\t\"baseUrl\": \"http://localhost:11434/v1\",\n\t\t\t\"apiKey\": \"ollama\"\n\t\t}\n\t}\n}"
	writeFixture(t, path, original)
	layout := Layout{Root: root}
	if err := layout.Connect("pi", "http://127.0.0.1:8317/v1", ""); err != nil {
		t.Fatal(err)
	}
	want := "{\n\t\"providers\": {\n\t\t\"ollama\": {\n\t\t\t\"baseUrl\": \"http://localhost:11434/v1\",\n\t\t\t\"apiKey\": \"ollama\"\n\t\t},\n" +
		"\t\t\"anthropic\": {\n\t\t\t\"baseUrl\": \"http://127.0.0.1:8317\",\n\t\t\t\"apiKey\": \"peaproxy\"\n\t\t},\n" +
		"\t\t\"openai\": {\n\t\t\t\"baseUrl\": \"http://127.0.0.1:8317/v1\",\n\t\t\t\"apiKey\": \"peaproxy\"\n\t\t}\n\t}\n}"
	assertFile(t, path, want)
	if err := layout.Disconnect("pi"); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, original)
}

func TestDisconnectWithoutOwnedKeysIsNoop(t *testing.T) {
	root := t.TempDir()
	layout := Layout{Root: root}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	for name, text := range map[string]string{
		"claude-code": "{\n    \"theme\": \"dark\",\n    \"env\": {\"FOO\": \"1\"}\n}\n",
		"opencode":    "{\n  \"theme\":   \"dark\"\n}",
		"pi":          "{\"providers\": {\"ollama\": {\"apiKey\": \"ollama\"}}}",
	} {
		path := layout.path(name)
		writeFixture(t, path, text)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		if err := layout.Disconnect(name); err != nil {
			t.Fatal(name, err)
		}
		assertFile(t, path, text)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(old) {
			t.Fatalf("%s: disconnect rewrote a file it does not own (mtime %v)", name, info.ModTime())
		}
	}
}

func TestMidFileCommentIsHoistedNotLost(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	writeFixture(t, path, "{\n  // why dark\n  \"theme\": \"dark\"\n}\n")
	if err := (Layout{Root: root}).Connect("claude-code", "http://127.0.0.1:8317/v1", ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "  // why dark\n{\n") {
		t.Fatalf("comment not hoisted above the object: %s", got)
	}
}

func TestClaudeCodeConnectRejectsNonObjectEnv(t *testing.T) {
	for _, env := range []string{`[1]`, `"x"`, `true`, `1`} {
		root := t.TempDir()
		path := filepath.Join(root, ".claude", "settings.json")
		original := `{"theme":"dark","env":` + env + "}\n"
		writeFixture(t, path, original)
		if err := (Layout{Root: root}).Connect("claude-code", "http://127.0.0.1:8317/v1", "m"); !errors.Is(err, ErrUnexpectedShape) {
			t.Fatalf("env %s: connect = %v, want ErrUnexpectedShape", env, err)
		}
		assertFile(t, path, original)
	}
}

func TestClaudeCodeConnectTreatsNullEnvAsEmpty(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	writeFixture(t, path, `{"env":null}`)
	if err := (Layout{Root: root}).Connect("claude-code", "http://127.0.0.1:8317/v1", "m"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("settings invalid: %v\n%s", err, raw)
	}
	if parsed.Env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:8317" || parsed.Env["ANTHROPIC_API_KEY"] != "peaproxy" || parsed.Env["ANTHROPIC_MODEL"] != "m" {
		t.Fatalf("null env not replaced: %s", raw)
	}
}

func TestPiConnectRejectsNonObjectProviders(t *testing.T) {
	for _, original := range []string{`{"providers":[]}`, `{"providers":{"anthropic":1}}`, `{"providers":{"openai":"x"}}`} {
		root := t.TempDir()
		path := filepath.Join(root, ".pi", "agent", "models.json")
		writeFixture(t, path, original)
		if err := (Layout{Root: root}).Connect("pi", "http://127.0.0.1:8317/v1", ""); !errors.Is(err, ErrUnexpectedShape) {
			t.Fatalf("%s: connect = %v, want ErrUnexpectedShape", original, err)
		}
		assertFile(t, path, original)
	}
}
