package oauth

import (
	"strings"
	"testing"
)

func TestLiabilityWarningCoversBanRiskAndOfficialKeys(t *testing.T) {
	w := LiabilityWarning()
	for _, want := range []string{
		"may violate",
		"not liable",
		"own risk",
		"console.anthropic.com/settings/keys",
		"platform.openai.com/api-keys",
		"adapter anthropic",
		"adapter openai",
	} {
		if !strings.Contains(w, want) {
			t.Fatalf("warning missing %q:\n%s", want, w)
		}
	}
}

func TestCallbackCodeFromURL(t *testing.T) {
	code, state, err := CallbackFromInput("http://localhost:54545/callback?code=abc%2Fdef&state=st")
	if err != nil {
		t.Fatal(err)
	}
	if code != "abc/def" || state != "st" {
		t.Fatalf("code=%q state=%q", code, state)
	}
	code, state, err = CallbackFromInput("just-the-code")
	if err != nil || code != "just-the-code" || state != "" {
		t.Fatalf("%q %q %v", code, state, err)
	}
}
