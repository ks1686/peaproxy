package clients

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readAt(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// #58: formatJSON re-indented the whole document, so a compact array the user
// wrote by hand came back expanded across three lines.
func TestClaudeCodeRoundTripKeepsCompactArraysCompact(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	original := "{\n  \"allow\": [\"Bash(ls:*)\", \"Read(~/notes.md)\"],\n  \"env\": {\n    \"FOO\": \"bar\"\n  }\n}\n"
	writeAt(t, path, original)
	layout := Layout{Root: root}
	if err := layout.Connect("claude-code", "http://127.0.0.1:8317/v1", "m"); err != nil {
		t.Fatal(err)
	}
	connected := readAt(t, path)
	if !strings.Contains(connected, `"allow": ["Bash(ls:*)", "Read(~/notes.md)"]`) {
		t.Fatalf("connect reflowed a compact array:\n%s", connected)
	}
	if err := layout.Disconnect("claude-code"); err != nil {
		t.Fatal(err)
	}
	if got := readAt(t, path); got != original {
		t.Fatalf("round trip changed the file:\n--- got ---\n%q\n--- want ---\n%q", got, original)
	}
}

// #58: a nested object the user wrote inline stays inline, and a file that was
// already fully expanded is not re-indented into a different shape.
func TestClaudeCodeRoundTripKeepsInlineNestedObjects(t *testing.T) {
	for _, original := range []string{
		"{\n  \"permissions\": { \"allow\": [\"Bash(ls:*)\"] },\n  \"env\": {\n    \"FOO\": \"bar\"\n  }\n}\n",
		"{\n  \"env\": { \"FOO\": \"bar\" },\n  \"x\": 1\n}\n",
	} {
		t.Run(original, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".claude", "settings.json")
			writeAt(t, path, original)
			layout := Layout{Root: root}
			if err := layout.Connect("claude-code", "http://127.0.0.1:8317/v1", "m"); err != nil {
				t.Fatal(err)
			}
			if err := layout.Disconnect("claude-code"); err != nil {
				t.Fatal(err)
			}
			if got := readAt(t, path); got != original {
				t.Fatalf("round trip changed the file:\n--- got ---\n%q\n--- want ---\n%q", got, original)
			}
		})
	}
}

// #58: the TOML block was appended with no blank line before it, and
// disconnect dropped the file's trailing newline.
func TestCodexRoundTripIsByteForByte(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".codex", "config.toml")
	original := "model = \"gpt-5\"\n\n[tools]\nweb_search = true\n"
	writeAt(t, path, original)
	layout := Layout{Root: root}
	if err := layout.Connect("codex", "http://127.0.0.1:8317/v1", "m"); err != nil {
		t.Fatal(err)
	}
	connected := readAt(t, path)
	if !strings.Contains(connected, "web_search = true\n\n[model_providers.peaproxy]") {
		t.Fatalf("the peaproxy block needs a blank line before it:\n%s", connected)
	}
	if err := layout.Disconnect("codex"); err != nil {
		t.Fatal(err)
	}
	if got := readAt(t, path); got != original {
		t.Fatalf("round trip changed the file:\n--- got ---\n%q\n--- want ---\n%q", got, original)
	}
}

// A file with no trailing newline keeps having none.
func TestCodexRoundTripKeepsMissingTrailingNewline(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".codex", "config.toml")
	original := "[tools]\nweb_search = true"
	writeAt(t, path, original)
	layout := Layout{Root: root}
	if err := layout.Connect("codex", "http://127.0.0.1:8317/v1", "m"); err != nil {
		t.Fatal(err)
	}
	if err := layout.Disconnect("codex"); err != nil {
		t.Fatal(err)
	}
	if got := readAt(t, path); got != original {
		t.Fatalf("round trip changed the file:\n--- got ---\n%q\n--- want ---\n%q", got, original)
	}
}

// CRLF survives both directions in the TOML client too, not just in JSON.
func TestCodexRoundTripPreservesCRLF(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".codex", "config.toml")
	original := "model = \"gpt-5\"\r\n\r\n[tools]\r\nweb_search = true\r\n"
	writeAt(t, path, original)
	layout := Layout{Root: root}
	if err := layout.Connect("codex", "http://127.0.0.1:8317/v1", "m"); err != nil {
		t.Fatal(err)
	}
	connected := readAt(t, path)
	if n := strings.Count(connected, "\n"); n != strings.Count(connected, "\r\n") {
		t.Fatalf("connect lost CRLF:\n%q", connected)
	}
	if err := layout.Disconnect("codex"); err != nil {
		t.Fatal(err)
	}
	if got := readAt(t, path); got != original {
		t.Fatalf("round trip changed the file:\n--- got ---\n%q\n--- want ---\n%q", got, original)
	}
}

// #56: --origin means the bare origin everywhere, but connect used to write
// whatever it was given verbatim. A user copying the origin that
// "clients show" prints got an OpenAI client with no /v1, which cannot work.
func TestConnectAcceptsEitherOriginForm(t *testing.T) {
	for _, client := range []string{"codex", "continue"} {
		for _, origin := range []string{
			"http://127.0.0.1:8317",
			"http://127.0.0.1:8317/",
			"http://127.0.0.1:8317/v1",
			"http://127.0.0.1:8317/v1/",
		} {
			t.Run(client+" "+origin, func(t *testing.T) {
				root := t.TempDir()
				layout := Layout{Root: root}
				var path string
				switch client {
				case "codex":
					path = filepath.Join(root, ".codex", "config.toml")
					writeAt(t, path, "model = \"gpt-5\"\n")
				case "continue":
					path = filepath.Join(root, ".continue", "config.yaml")
					writeAt(t, path, "models:\n  - name: Mine\n")
				}
				if err := layout.Connect(client, origin, "m"); err != nil {
					t.Fatal(err)
				}
				got := readAt(t, path)
				if !strings.Contains(got, "http://127.0.0.1:8317/v1") {
					t.Fatalf("the OpenAI wire needs the /v1 suffix, got:\n%s", got)
				}
				if strings.Contains(got, "/v1/v1") {
					t.Fatalf("the suffix was doubled:\n%s", got)
				}
			})
		}
	}
}

// #56: Claude Code appends /v1/messages itself, so its base URL must NOT carry
// /v1 no matter which form the user passed.
func TestConnectStripsV1ForClaudeCode(t *testing.T) {
	for _, origin := range []string{
		"http://127.0.0.1:8317",
		"http://127.0.0.1:8317/",
		"http://127.0.0.1:8317/v1",
		"http://127.0.0.1:8317/v1/",
	} {
		t.Run(origin, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".claude", "settings.json")
			writeAt(t, path, "{\n  \"env\": {}\n}\n")
			layout := Layout{Root: root}
			if err := layout.Connect("claude-code", origin, "m"); err != nil {
				t.Fatal(err)
			}
			got := readAt(t, path)
			if !strings.Contains(got, `"ANTHROPIC_BASE_URL": "http://127.0.0.1:8317"`) {
				t.Fatalf("claude-code must not get a /v1 base URL:\n%s", got)
			}
		})
	}
}

// Pi writes both an anthropic entry without /v1 and an openai entry with it,
// from one --origin, whichever form it was given.
func TestConnectPiWiresBothWiresFromEitherForm(t *testing.T) {
	for _, origin := range []string{"http://127.0.0.1:8317", "http://127.0.0.1:8317/v1"} {
		t.Run(origin, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".pi", "agent", "models.json")
			writeAt(t, path, "{\n  \"providers\": {}\n}\n")
			layout := Layout{Root: root}
			if err := layout.Connect("pi", origin, ""); err != nil {
				t.Fatal(err)
			}
			got := readAt(t, path)
			if !strings.Contains(got, `"anthropic"`) || !strings.Contains(got, `"openai"`) {
				t.Fatalf("pi needs both providers:\n%s", got)
			}
			if strings.Contains(got, "/v1/v1") {
				t.Fatalf("the suffix was doubled:\n%s", got)
			}
			// The anthropic entry is the one that must not carry /v1.
			anth := got[strings.Index(got, `"anthropic"`):]
			anth = anth[:strings.Index(anth, "}")]
			if !strings.Contains(anth, `"baseUrl": "http://127.0.0.1:8317"`) {
				t.Fatalf("the anthropic entry must not carry /v1:\n%s", anth)
			}
		})
	}
}

// #56: one helper, so the CLI and the server cannot drift.
func TestNormalizeOrigin(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://127.0.0.1:8317", "http://127.0.0.1:8317"},
		{"http://127.0.0.1:8317/", "http://127.0.0.1:8317"},
		{"http://127.0.0.1:8317/v1", "http://127.0.0.1:8317"},
		{"http://127.0.0.1:8317/v1/", "http://127.0.0.1:8317"},
		{"  http://127.0.0.1:8317/v1  ", "http://127.0.0.1:8317"},
		{"http://host:1234/v1/", "http://host:1234"},
		// Only one trailing /v1 is stripped: a path that merely ends in the
		// letters v1 is left alone.
		{"http://host/api/v1/v1", "http://host/api/v1"},
		{"", ""},
		{"http://host/v1beta", "http://host/v1beta"},
	} {
		if got := NormalizeOrigin(tc.in); got != tc.want {
			t.Errorf("NormalizeOrigin(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
