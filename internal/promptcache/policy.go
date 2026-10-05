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

// insertBreakpoint adds one ephemeral marker on the last system block,
// changing no other byte of the request.
//
// A prompt cache only hits when the bytes before the marker are identical to
// the previous turn. An earlier version unmarshalled the body into a map and
// marshalled it back, which sorted every key and dropped the caller's
// formatting -- so the prefix changed on every request and the cache could not
// hit, silently, with requests still succeeding. The insert is therefore made
// on the original bytes: the surrounding request is copied verbatim.
//
// Message order is unchanged, and unknown shapes are left untouched.
func insertBreakpoint(body []byte) ([]byte, error) {
	start, end, ok := objectValueSpan(body, "system")
	if !ok {
		return body, nil
	}
	elemStart, elemEnd, ok := lastArrayElementSpan(body[start:end])
	if !ok {
		return body, nil
	}
	elem := body[start+elemStart : start+elemEnd]

	if hasJSONKey(elem, "cache_control") {
		return body, nil
	}
	closing := bytes.LastIndexByte(elem, '}')
	if closing < 0 {
		return body, nil
	}
	insert := []byte(`,"cache_control":{"type":"ephemeral"}`)
	if isEmptyObject(elem) {
		// An empty block would otherwise begin with a comma.
		insert = []byte(`"cache_control":{"type":"ephemeral"}`)
	}

	at := start + elemEnd - len(elem) + closing
	out := make([]byte, 0, len(body)+len(insert))
	out = append(out, body[:at]...)
	out = append(out, insert...)
	out = append(out, body[at:]...)
	return out, nil
}

// objectValueSpan returns the byte range of one top-level key's value.
func objectValueSpan(body []byte, want string) (start, end int, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return 0, 0, false
	}
	if d, isDelim := tok.(json.Delim); !isDelim || d != '{' {
		return 0, 0, false
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return 0, 0, false
		}
		key, _ := keyTok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return 0, 0, false
		}
		endOff := int(dec.InputOffset())
		if key == want {
			return endOff - len(raw), endOff, true
		}
	}
	return 0, 0, false
}

// lastArrayElementSpan returns the byte range of an array's final element,
// with trailing whitespace excluded.
func lastArrayElementSpan(arr []byte) (start, end int, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(arr))
	tok, err := dec.Token()
	if err != nil {
		return 0, 0, false
	}
	if d, isDelim := tok.(json.Delim); !isDelim || d != '[' {
		return 0, 0, false
	}
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return 0, 0, false
		}
		endOff := int(dec.InputOffset())
		start, end = endOff-len(raw), endOff
		ok = true
	}
	if !ok {
		return 0, 0, false
	}
	for end > start && (arr[end-1] == ' ' || arr[end-1] == '\n' || arr[end-1] == '\t' || arr[end-1] == '\r') {
		end--
	}
	return start, end, true
}

// hasJSONKey reports whether an object already carries a key. Only used to
// decide whether to insert; it never contributes to the returned bytes.
func hasJSONKey(obj []byte, want string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(obj, &m); err != nil {
		return false
	}
	_, ok := m[want]
	return ok
}

func isEmptyObject(obj []byte) bool {
	trimmed := bytes.TrimSpace(obj)
	return len(trimmed) == 2 && trimmed[0] == '{' && trimmed[1] == '}'
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
