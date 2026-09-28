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
