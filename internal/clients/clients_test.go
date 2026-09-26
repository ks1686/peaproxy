package clients

import "testing"

func TestListIncludesHarnessesFromPlan(t *testing.T) {
	got := map[string]bool{}
	for _, n := range List() {
		got[n] = true
	}
	for _, want := range []string{"cursor", "claude-code", "opencode", "pi", "codex", "continue"} {
		if !got[want] {
			t.Fatalf("missing preset %s in %v", want, List())
		}
	}
}

func TestGetPiDocumentsCloakWarning(t *testing.T) {
	p, ok := Get("pi")
	if !ok {
		t.Fatal("missing pi")
	}
	if p.Notes == "" {
		t.Fatal("pi notes must mention cloak defaults")
	}
}
