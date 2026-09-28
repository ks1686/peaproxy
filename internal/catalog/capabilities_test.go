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
