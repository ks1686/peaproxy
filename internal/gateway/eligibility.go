package gateway

import (
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/requestmeta"
)

// routedRequirements are the capabilities a request can need that routing can
// rule on.
func routedRequirements(r requestmeta.Requirements) map[catalog.Requirement]bool {
	return map[catalog.Requirement]bool{
		catalog.RequirementTools:         r.Tools,
		catalog.RequirementParallelTools: r.ParallelTools,
		catalog.RequirementStrictSchema:  r.StrictSchema,
		catalog.RequirementVision:        r.Vision,
		catalog.RequirementContinuation:  r.Continuation,
	}
}

// blocks reports whether a deployment has been told it cannot do this request.
//
// It refuses on a stated no and permits unknown, on both the exact-model and
// the automatic path.
//
// v3.0.4 required positive evidence here and applied this function to exact-
// model routing too, despite the comment above it promising that path stayed
// permissive. That made strict-schema, parallel-tools and continuation requests
// unroutable: no evidence for those can ever exist (no model-listing API
// reports them and no compat profile carries the field), so every deployment
// was excluded and the request was refused with advice to configure a field the
// configuration does not have. A client that sends strict tools -- pi does --
// simply stopped working.
//
// The original defect this guarded is still guarded. A provider configured
// capabilities.tools: false is still refused a tool request on either path; the
// difference is that "nobody said" is no longer read as "said no".
func blocks(evidence catalog.CapabilityEvidence, requirements requestmeta.Requirements) (catalog.Requirement, bool) {
	for requirement, needed := range routedRequirements(requirements) {
		if needed && evidence.Disallows(requirement) {
			return requirement, true
		}
	}
	return "", false
}
