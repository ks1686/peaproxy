package catalog

import "github.com/ks1686/peaproxy/internal/compatdata"

// Support is explicit capability evidence. Unknown is intentionally distinct
// from yes so automatic routing does not over-promise compatibility.
type Support string

const (
	SupportUnknown Support = "unknown"
	SupportYes     Support = "yes"
	SupportNo      Support = "no"
)

// Requirement names a request feature used for candidate eligibility.
type Requirement string

const (
	RequirementTools         Requirement = "tools"
	RequirementParallelTools Requirement = "parallel_tools"
	RequirementStrictSchema  Requirement = "strict_schema"
	RequirementVision        Requirement = "vision"
	RequirementContinuation  Requirement = "continuation"
)

// CapabilityEvidence describes what a model/adapter combination is known to
// support. Source and observation time are added with live metadata in the
// routing slice; this minimal contract prevents bool defaults from meaning yes.
type CapabilityEvidence struct {
	Tools         Support `json:"tools"`
	ParallelTools Support `json:"parallelTools"`
	StrictSchema  Support `json:"strictSchema"`
	Vision        Support `json:"vision"`
	Continuation  Support `json:"continuation"`
}

// State returns known support for a requirement.
func (c CapabilityEvidence) State(requirement Requirement) Support {
	switch requirement {
	case RequirementTools:
		return c.Tools
	case RequirementParallelTools:
		return c.ParallelTools
	case RequirementStrictSchema:
		return c.StrictSchema
	case RequirementVision:
		return c.Vision
	case RequirementContinuation:
		return c.Continuation
	default:
		return SupportUnknown
	}
}

// Supports is suitable for automatic routing, which requires positive evidence.
func (c CapabilityEvidence) Supports(requirement Requirement) bool {
	return c.State(requirement) == SupportYes
}

// FillUnknown applies a bundled profile only where evidence is still unknown.
// A profile cannot create catalog models, and known live evidence wins.
func FillUnknown(profile string, ev CapabilityEvidence) CapabilityEvidence {
	if profile == "" {
		return ev
	}
	fact, ok := compatdata.Bundled().Profiles[profile]
	if !ok {
		return ev
	}
	ev.Tools = fillSupport(ev.Tools, fact.Tools)
	ev.Vision = fillSupport(ev.Vision, fact.Vision)
	return ev
}

func fillSupport(current Support, fact string) Support {
	if current == SupportYes || current == SupportNo {
		return current
	}
	switch fact {
	case "yes":
		return SupportYes
	case "no":
		return SupportNo
	default:
		return SupportUnknown
	}
}
