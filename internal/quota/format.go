package quota

import (
	"fmt"
	"strings"
)

// Format is a one-line CLI summary. Unknown remaining is never printed as 0.
func (s Snapshot) Format() string {
	if !s.Reported() {
		if s.Note != "" {
			return s.Note
		}
		return "not reported by provider"
	}
	var parts []string
	addInt := func(k string, v *int64) {
		if v != nil {
			parts = append(parts, fmt.Sprintf("%s=%d", k, *v))
		}
	}
	addFloat := func(k string, v *float64) {
		if v != nil {
			parts = append(parts, fmt.Sprintf("%s=%g", k, *v))
		}
	}
	addInt("remainingRequests", s.RemainingRequests)
	addInt("remainingTokens", s.RemainingTokens)
	addInt("remainingRequestsDay", s.RemainingRequestsDay)
	addInt("remainingTokensMinute", s.RemainingTokensMinute)
	addInt("remainingInputTokens", s.RemainingInputTokens)
	addInt("remainingOutputTokens", s.RemainingOutputTokens)
	addFloat("remainingCredits", s.RemainingCredits)
	addFloat("limitCredits", s.LimitCredits)
	if s.CreditsUnlimited {
		parts = append(parts, "credits=unlimited")
	}
	if s.Model != "" {
		parts = append(parts, "model="+s.Model)
	}
	if len(parts) == 0 {
		if s.Note != "" {
			return s.Note
		}
		return "not reported by provider"
	}
	return strings.Join(parts, " ")
}

// Compact is a short Health/request-log hint. Unknown remaining is empty —
// never printed as 0. Honest 0 from the provider is shown as req=0.
func (s Snapshot) Compact() string {
	if !s.Reported() {
		return ""
	}
	var parts []string
	addInt := func(k string, v *int64) {
		if v != nil {
			parts = append(parts, fmt.Sprintf("%s=%d", k, *v))
		}
	}
	addFloat := func(k string, v *float64) {
		if v != nil {
			parts = append(parts, fmt.Sprintf("%s=%g", k, *v))
		}
	}
	addInt("req", s.RemainingRequests)
	addInt("tok", s.RemainingTokens)
	addInt("req/day", s.RemainingRequestsDay)
	addInt("tok/min", s.RemainingTokensMinute)
	addInt("in", s.RemainingInputTokens)
	addInt("out", s.RemainingOutputTokens)
	addFloat("credits", s.RemainingCredits)
	if s.CreditsUnlimited {
		parts = append(parts, "credits=unlimited")
	}
	return strings.Join(parts, " ")
}
