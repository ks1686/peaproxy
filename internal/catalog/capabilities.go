package catalog

import (
	"strings"

	"github.com/ks1686/peaproxy/internal/compatdata"
)

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
	RequirementContinuation  Requirement = "continition"
	// RequirementVerbosity is text.verbosity on a Responses request. It is a
	// chat field Codex clients send that has no meaning on a model that never
	// implemented it (#60).
	RequirementVerbosity Requirement = "verbosity"
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
	Verbosity     Support `json:"verbosity"`
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
	case RequirementVerbosity:
		return c.Verbosity
	default:
		return SupportUnknown
	}
}

// Supports is suitable for automatic routing, which requires positive evidence.
func (c CapabilityEvidence) Supports(requirement Requirement) bool {
	return c.State(requirement) == SupportYes
}

// Set records evidence for a requirement, ignoring unknown.
func (c *CapabilityEvidence) Set(requirement Requirement, support Support) {
	switch requirement {
	case RequirementTools:
		c.Tools = support
	case RequirementParallelTools:
		c.ParallelTools = support
	case RequirementStrictSchema:
		c.StrictSchema = support
	case RequirementVision:
		c.Vision = support
	case RequirementContinuation:
		c.Continuation = support
	case RequirementVerbosity:
		c.Verbosity = support
	}
}

// Disallows reports a stated inability. This is the only negative signal
// PeaProxy can act on, and it is deliberately the mirror of Supports rather
// than its negation.
//
// The difference matters because most requirements have no evidence to give.
// Nothing observes whether an endpoint honours a strict JSON schema, because no
// model-listing API reports it, and no compat profile carries the field. For
// those, State is always unknown -- so treating unknown as inability refuses
// every deployment for a request nothing can satisfy, which is what v3.0.4 did
// to every strict-tools call. Nobody said no; nothing has yet said yes either,
// and routing is a guess the provider is better placed to settle.
//
// Flat bools cannot help here. Several adapters omit Tools entirely, so
// "declared false" and "never declared" are the same value, and reading a
// missing field as a statement would refuse adapters that do support tools.
// SupportNo therefore comes only from an explicit declaration in config.
func (c CapabilityEvidence) Disallows(requirement Requirement) bool {
	return c.State(requirement) == SupportNo
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

// verbosityModels are the model families Codex sends text.verbosity to. It is a
// Responses field with no chat-completions equivalent, and sending it to a
// model that does not implement it is a 400 rather than a no-op.
//
// Deliberately a positive list. The cost of being wrong is asymmetric: omitting
// a model that does support it costs one unset field, while including one that
// does not costs the request.
var verbosityModels = []string{"gpt-5", "gpt-6", "gpt-daybreak", "codex-auto-review"}

// SupportsVerbosity reports whether a model id takes text.verbosity.
func SupportsVerbosity(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, prefix := range verbosityModels {
		if id == prefix || strings.HasPrefix(id, prefix+"-") || strings.HasPrefix(id, prefix+"_") {
			return true
		}
	}
	return false
}
