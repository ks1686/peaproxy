package gateway

import (
	"testing"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/requestmeta"
)

// Routing refuses on a stated no and permits unknown.
//
// v3.0.4 required positive evidence here, which made strict-schema,
// parallel-tools and continuation requests unroutable everywhere: no evidence for
// those can exist, so every deployment was excluded. The unit boundary is
// tested here because the gateway-level tests only cover the two requirements
// someone can actually declare.

func TestUnknownEvidencePermitsRouting(t *testing.T) {
	for _, r := range []requestmeta.Requirements{
		{Tools: true},
		{StrictSchema: true},
		{ParallelTools: true},
		{Continuation: true},
	} {
		if _, blocked := blocks(catalog.CapabilityEvidence{}, r); blocked {
			t.Fatalf("%+v was refused on evidence nobody gave", r)
		}
	}
}

func TestAStatedNoBlocksRouting(t *testing.T) {
	evidence := catalog.CapabilityEvidence{Tools: catalog.SupportNo}
	got, blocked := blocks(evidence, requestmeta.Requirements{Tools: true})
	if !blocked {
		t.Fatal("a deployment declared tools: false was allowed a tool request")
	}
	if got != catalog.RequirementTools {
		t.Fatalf("blocked on %q, want tools", got)
	}
}

// A stated no for something the request does not need must not block anything.
func TestAStatedNoForAnUnusedRequirementDoesNotBlock(t *testing.T) {
	evidence := catalog.CapabilityEvidence{Tools: catalog.SupportNo}
	if _, blocked := blocks(evidence, requestmeta.Requirements{Vision: true}); blocked {
		t.Fatal("a tools: false declaration blocked a vision request")
	}
}

// Positive evidence and unknown both permit; only a stated no refuses.
func TestPositiveEvidenceStillPermits(t *testing.T) {
	evidence := catalog.CapabilityEvidence{Tools: catalog.SupportYes, StrictSchema: catalog.SupportYes}
	if _, blocked := blocks(evidence, requestmeta.Requirements{Tools: true, StrictSchema: true}); blocked {
		t.Fatal("positive evidence was refused")
	}
}

func TestNoRequirementsMeansNothingBlocks(t *testing.T) {
	if _, blocked := blocks(catalog.CapabilityEvidence{Tools: catalog.SupportNo}, requestmeta.Requirements{}); blocked {
		t.Fatal("a request needing nothing was blocked")
	}
}
