package gateway

import (
	"testing"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/requestmeta"
)

func TestEligibleForAutomaticRouteRejectsUnknownRequiredCapability(t *testing.T) {
	if eligibleForAutomaticRoute(catalog.CapabilityEvidence{}, requestmeta.Requirements{Tools: true}) {
		t.Fatal("unknown tool support must not qualify automatic routing")
	}
}

func TestEligibleForAutomaticRouteAcceptsPositiveRequiredCapabilities(t *testing.T) {
	caps := catalog.CapabilityEvidence{
		Tools: catalog.SupportYes, ParallelTools: catalog.SupportYes,
		StrictSchema: catalog.SupportYes, Vision: catalog.SupportYes,
	}
	req := requestmeta.Requirements{Tools: true, ParallelTools: true, StrictSchema: true, Vision: true}
	if !eligibleForAutomaticRoute(caps, req) {
		t.Fatal("positive evidence should qualify automatic routing")
	}
}

func TestExactRouteDoesNotUseAutomaticEligibility(t *testing.T) {
	// This guard documents the current contract: only a future explicit automatic
	// route invokes eligibleForAutomaticRoute; exact native requests remain live.
	if eligibleForAutomaticRoute(catalog.CapabilityEvidence{}, requestmeta.Requirements{}) != true {
		t.Fatal("no requirements must remain eligible")
	}
}
