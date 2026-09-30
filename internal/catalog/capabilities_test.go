package catalog

import "testing"

func TestCapabilityEvidenceEligibilityRequiresPositiveSupport(t *testing.T) {
	caps := CapabilityEvidence{Tools: SupportUnknown}
	if caps.Supports(RequirementTools) {
		t.Fatal("unknown tool support must not qualify an automatic route")
	}
	caps.Tools = SupportYes
	if !caps.Supports(RequirementTools) {
		t.Fatal("positive tool support must qualify")
	}
}

func TestBundledProfileFillsUnknownOnly(t *testing.T) {
	filled := FillUnknown("anthropic-claude", CapabilityEvidence{})
	if filled.Tools != SupportYes || filled.Vision != SupportYes {
		t.Fatalf("bundled profile did not fill unknown facts: %+v", filled)
	}
	kept := FillUnknown("anthropic-claude", CapabilityEvidence{Tools: SupportNo})
	if kept.Tools != SupportNo {
		t.Fatalf("live no was overwritten: %+v", kept)
	}
	if got := FillUnknown("missing", CapabilityEvidence{}); got.Tools != "" && got.Tools != SupportUnknown {
		t.Fatalf("unknown profile invented support: %+v", got)
	}
}

func TestEligibilityUnknownIsNotSupported(t *testing.T) {
	if (CapabilityEvidence{}).Supports(RequirementTools) {
		t.Fatal("unknown capability counted as support")
	}
}

func TestCapabilityEvidenceReportsKnownUnsupported(t *testing.T) {
	caps := CapabilityEvidence{StrictSchema: SupportNo}
	if got := caps.State(RequirementStrictSchema); got != SupportNo {
		t.Fatalf("state = %q, want no", got)
	}
}

// #60: text.verbosity only goes to models that implement it. A positive list,
// because omitting a model costs an unset field and including one that does not
// support it costs the request.
func TestSupportsVerbosity(t *testing.T) {
	for _, id := range []string{
		"gpt-5", "gpt-5-codex", "gpt-5-mini", "gpt-6", "gpt-6-codex",
		"gpt-daybreak", "codex-auto-review", "GPT-5-Codex", " gpt-6 ",
	} {
		if !SupportsVerbosity(id) {
			t.Errorf("%q supports verbosity but was not recognised", id)
		}
	}
	for _, id := range []string{"o3", "o4-mini", "gpt-4.1", "gpt-4o", "claude-sonnet-5-5", "gpt-50", ""} {
		if SupportsVerbosity(id) {
			t.Errorf("%q does not support verbosity but was treated as if it did", id)
		}
	}
}
