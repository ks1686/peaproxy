// Package promptcache decides whether PeaProxy may add provider cache directives.
// Preserve is the default and does not rewrite caller bytes.
package promptcache

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/ks1686/peaproxy/internal/compatdata"
)

// Mode controls PeaProxy-owned cache edits.
type Mode string

const (
	ModePreserve Mode = "preserve"
	ModeOptimize Mode = "optimize"
	ModeOff      Mode = "off"
)

// ErrUnknownMode is returned for a mode other than preserve, optimize, or off.
var ErrUnknownMode = errors.New("unknown prompt cache mode")

// Profile is a documented model/endpoint cache shape. MaxBreakpoints is zero
// when PeaProxy must not add directives.
type Profile struct {
	Name           string
	MaxBreakpoints int
}

// Known profiles. Anything else is treated as unknown.
var profiles = map[string]Profile{
	"anthropic-claude": {Name: "anthropic-claude", MaxBreakpoints: 4},
}

func init() {
	ApplyDocument(compatdata.Bundled())
}

// ApplyDocument turns off added breakpoints when a bundled profile says cache is off.
// It does not create profiles, so metadata cannot invent an optimize target.
func ApplyDocument(doc compatdata.Document) {
	for name, fact := range doc.Profiles {
		cur, ok := profiles[name]
		if !ok {
			continue
		}
		if fact.Cache == "off" {
			cur.MaxBreakpoints = 0
			profiles[name] = cur
		}
	}
}

// NormalizeMode returns preserve for an empty value.
func NormalizeMode(mode string) (Mode, error) {
	switch Mode(mode) {
	case "", ModePreserve:
		return ModePreserve, nil
	case ModeOptimize:
		return ModeOptimize, nil
	case ModeOff:
		return ModeOff, nil
	default:
		return "", ErrUnknownMode
	}
}

// Counters are provider cache usage after removing double-counted totals.
type Counters struct {
	Read     int
	Write    int
	Uncached int
}

// NormalizeCounters keeps read and write separate from an uncached total.
// A provider total that already includes cache reads is not added again.
func NormalizeCounters(read, write, uncached, providerTotal int) Counters {
	if providerTotal > 0 && providerTotal == read+write+uncached {
		return Counters{Read: read, Write: write, Uncached: uncached}
	}
	if providerTotal > 0 && uncached == 0 && providerTotal >= read+write {
		uncached = providerTotal - read - write
	}
	if uncached < 0 {
		uncached = 0
	}
	return Counters{Read: read, Write: write, Uncached: uncached}
}

// Apply returns the body to send upstream. Preserve and off do not add
// directives. Off also leaves caller-owned cache_control bytes in place.
// Optimize adds nothing for an unknown profile or when the breakpoint limit
// is already met.
func Apply(body []byte, mode Mode, profile string) ([]byte, error) {
	switch mode {
	case "", ModePreserve, ModeOff:
		return body, nil
	case ModeOptimize:
	default:
		return body, ErrUnknownMode
	}
	spec, ok := profiles[profile]
	if !ok || spec.MaxBreakpoints <= 0 {
		return body, nil
	}
	if bytes.Count(body, []byte("cache_control")) >= spec.MaxBreakpoints {
		return body, nil
	}
	return insertBreakpoint(body)
}

// insertBreakpoint adds one ephemeral marker on the last system block.
// Message order is unchanged. Unknown shapes are left untouched.
func insertBreakpoint(body []byte) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return body, nil
	}
	raw, ok := doc["system"]
	if !ok {
		return body, nil
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil || len(blocks) == 0 {
		return body, nil
	}
	last := blocks[len(blocks)-1]
	if _, exists := last["cache_control"]; exists {
		return body, nil
	}
	last["cache_control"] = json.RawMessage(`{"type":"ephemeral"}`)
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return body, nil
	}
	doc["system"] = encoded
	out, err := json.Marshal(doc)
	if err != nil {
		return body, nil
	}
	return out, nil
}

// ProfileForAdapter maps an adapter id to a documented profile, or empty.
func ProfileForAdapter(adapter string) string {
	switch adapter {
	case "anthropic", "anthropic_oauth":
		return "anthropic-claude"
	default:
		return ""
	}
}
