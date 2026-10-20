package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/usage"
)

// extraUsageType is the error type and code a client sees when Anthropic
// refuses to bill a subscription request against the plan limits.
const extraUsageType = "extra_usage_required"

// maxExtraUsageMessage bounds the quoted upstream sentence. Anthropic's is
// about 120 characters; anything much longer is not the message we matched.
const maxExtraUsageMessage = 300

var (
	anthropicRequestIDRE  = regexp.MustCompile(`^req_[A-Za-z0-9_-]{1,128}$`)
	extraUsageMessageRE   = regexp.MustCompile(`"message"\s*:\s*("(?:[^"\\]|\\.)*")`)
	extraUsageRequestIDRE = regexp.MustCompile(`"request_id"\s*:\s*("(?:[^"\\]|\\.)*")`)
)

// jsonStringField returns the first complete JSON string re captures, decoded.
func jsonStringField(body string, re *regexp.Regexp) string {
	m := re.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	var v string
	if json.Unmarshal([]byte(m[1]), &v) != nil {
		return ""
	}
	return v
}

// extraUsage is the client-facing form of Anthropic's "Third-party apps now
// draw from extra usage" refusal.
//
// Every other upstream body stays out of client errors (#70). This one is
// Anthropic's own product text, it is the only actionable fact about the
// failure, and replacing it with "upstream HTTP 400" sent people bisecting
// request shapes for hours. So the matched message is quoted after
// redaction; one that no longer carries the phrase, or runs past
// maxExtraUsageMessage, is replaced by a fixed sentence. The request_id is
// passed on only when it has Anthropic's shape.
type extraUsage struct {
	Status    int
	Message   string
	RequestID string
}

func extraUsageOf(err error) (extraUsage, bool) {
	if !router.ExtraUsageRequired(err) {
		return extraUsage{}, false
	}
	var he adapter.HTTPError
	if !errors.As(err, &he) {
		return extraUsage{}, false
	}
	out := extraUsage{Status: he.Status, Message: "Third-party apps now draw from extra usage, not plan limits."}
	// The adapters keep only the first 240 bytes of an error body, so the JSON
	// can be cut off after the fields we want. Each is read on its own and
	// only when its string value is complete.
	if m := jsonStringField(he.Body, extraUsageMessageRE); m != "" {
		if m = usage.Redact(m); router.ExtraUsageRequired(adapter.HTTPError{Status: he.Status, Body: m}) && len([]rune(m)) <= maxExtraUsageMessage {
			out.Message = m
		}
	}
	if id := jsonStringField(he.Body, extraUsageRequestIDRE); anthropicRequestIDRE.MatchString(id) {
		out.RequestID = id
	}
	return out, true
}

func (e extraUsage) clientMessage() string {
	msg := fmt.Sprintf("Anthropic refused this request against the subscription plan limits: %q "+
		"Use an Anthropic API-key account for this client, or ask the workspace admin to enable extra usage. "+
		"(upstream HTTP %d", e.Message, e.Status)
	if e.RequestID != "" {
		msg += ", request_id " + e.RequestID
	}
	return msg + ")"
}
