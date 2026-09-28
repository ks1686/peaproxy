package gateway

import (
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/requestmeta"
)

// eligibleForAutomaticRoute requires positive support evidence. Exact-model
// routing remains intentionally permissive because a native provider may
// support a feature that the local catalog has not observed yet.
func eligibleForAutomaticRoute(capabilities catalog.CapabilityEvidence, requirements requestmeta.Requirements) bool {
	for requirement, needed := range map[catalog.Requirement]bool{
		catalog.RequirementTools:         requirements.Tools,
		catalog.RequirementParallelTools: requirements.ParallelTools,
		catalog.RequirementStrictSchema:  requirements.StrictSchema,
		catalog.RequirementVision:        requirements.Vision,
		catalog.RequirementContinuation:  requirements.Continuation,
	} {
		if needed && !capabilities.Supports(requirement) {
			return false
		}
	}
	return true
}
