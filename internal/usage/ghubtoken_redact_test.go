package usage

import (
	"strings"
	"testing"
)

const ghToken = "gho_live_0123456789abcdef"

// A Copilot preview carries the GitHub token in the clear, and the inspector
// writes previews to disk, so Redact has to know the key.
func TestRedactStripsGitHubToken(t *testing.T) {
	in := `{"github_token":"` + ghToken + `","copilot_plan":"individual"}`
	out := Redact(in)
	if strings.Contains(out, ghToken) {
		t.Fatalf("github_token survived redaction: %s", out)
	}
	if !strings.Contains(out, "individual") {
		t.Fatalf("redaction ate the non-secret half: %s", out)
	}
}

// The key is matched case-insensitively, like every other secret key.
func TestRedactStripsGitHubTokenRegardlessOfCase(t *testing.T) {
	for _, key := range []string{"github_token", "GitHub_Token", "GITHUB_TOKEN"} {
		out := Redact(`{"` + key + `":"` + ghToken + `"}`)
		if strings.Contains(out, ghToken) {
			t.Fatalf("%s survived redaction: %s", key, out)
		}
	}
}
