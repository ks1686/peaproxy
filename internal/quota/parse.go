package quota

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	SourceNone    = "none"
	SourceHeaders = "headers"
	SourceProbe   = "probe"
)

// Snapshot is the latest remaining-quota view for one account.
// Pointer/omitempty fields are omitted when the provider did not send them.
// A missing remaining is unknown — never coerced to 0 or unlimited.
type Snapshot struct {
	AccountID string `json:"accountId"`
	Adapter   string `json:"adapter"`
	Model     string `json:"model,omitempty"`
	Source    string `json:"source"`
	Note      string `json:"note,omitempty"`

	RemainingRequests *int64 `json:"remainingRequests,omitempty"`
	LimitRequests     *int64 `json:"limitRequests,omitempty"`
	ResetRequests     string `json:"resetRequests,omitempty"`

	RemainingTokens *int64 `json:"remainingTokens,omitempty"`
	LimitTokens     *int64 `json:"limitTokens,omitempty"`
	ResetTokens     string `json:"resetTokens,omitempty"`

	RemainingProjectTokens *int64 `json:"remainingProjectTokens,omitempty"`
	LimitProjectTokens     *int64 `json:"limitProjectTokens,omitempty"`
	ResetProjectTokens     string `json:"resetProjectTokens,omitempty"`

	RemainingRequestsDay *int64 `json:"remainingRequestsDay,omitempty"`
	LimitRequestsDay     *int64 `json:"limitRequestsDay,omitempty"`
	ResetRequestsDay     string `json:"resetRequestsDay,omitempty"`

	RemainingRequestsMinute *int64 `json:"remainingRequestsMinute,omitempty"`
	LimitRequestsMinute     *int64 `json:"limitRequestsMinute,omitempty"`
	ResetRequestsMinute     string `json:"resetRequestsMinute,omitempty"`

	RemainingTokensMinute *int64 `json:"remainingTokensMinute,omitempty"`
	LimitTokensMinute     *int64 `json:"limitTokensMinute,omitempty"`
	ResetTokensMinute     string `json:"resetTokensMinute,omitempty"`

	RemainingInputTokens  *int64 `json:"remainingInputTokens,omitempty"`
	LimitInputTokens      *int64 `json:"limitInputTokens,omitempty"`
	RemainingOutputTokens *int64 `json:"remainingOutputTokens,omitempty"`
	LimitOutputTokens     *int64 `json:"limitOutputTokens,omitempty"`

	RemainingTokensPrompt    *int64 `json:"remainingTokensPrompt,omitempty"`
	RemainingTokensGenerated *int64 `json:"remainingTokensGenerated,omitempty"`

	RemainingCredits *float64 `json:"remainingCredits,omitempty"`
	LimitCredits     *float64 `json:"limitCredits,omitempty"`
	UsageCredits     *float64 `json:"usageCredits,omitempty"`
	// CreditsUnlimited is set only when the provider documented null remaining as unlimited
	// (OpenRouter GET /api/v1/key). It is never inferred from missing headers.
	CreditsUnlimited bool `json:"creditsUnlimited,omitempty"`

	CapturedAt time.Time `json:"capturedAt,omitempty"`
}

// Reported is true when at least one provider-supplied remaining/limit value is present.
func (s Snapshot) Reported() bool {
	return s.RemainingRequests != nil ||
		s.LimitRequests != nil ||
		s.RemainingTokens != nil ||
		s.LimitTokens != nil ||
		s.RemainingProjectTokens != nil ||
		s.RemainingRequestsDay != nil ||
		s.RemainingRequestsMinute != nil ||
		s.RemainingTokensMinute != nil ||
		s.RemainingInputTokens != nil ||
		s.RemainingOutputTokens != nil ||
		s.RemainingTokensPrompt != nil ||
		s.RemainingTokensGenerated != nil ||
		s.RemainingCredits != nil ||
		s.LimitCredits != nil ||
		s.UsageCredits != nil ||
		s.CreditsUnlimited ||
		s.ResetRequests != "" ||
		s.ResetTokens != ""
}

// ParseHeaders extracts documented remaining/limit headers. Unknown or unparsable
// values are left unset (not 0). An empty result means the provider did not report.
func ParseHeaders(h http.Header) Snapshot {
	if h == nil {
		return Snapshot{}
	}
	s := Snapshot{
		Source:                   SourceHeaders,
		RemainingRequests:        intHeader(h, "x-ratelimit-remaining-requests", "anthropic-ratelimit-requests-remaining", "x-ratelimit-remaining"),
		LimitRequests:            intHeader(h, "x-ratelimit-limit-requests", "anthropic-ratelimit-requests-limit", "x-ratelimit-limit"),
		ResetRequests:            strHeader(h, "x-ratelimit-reset-requests", "anthropic-ratelimit-requests-reset", "x-ratelimit-reset"),
		RemainingTokens:          intHeader(h, "x-ratelimit-remaining-tokens", "anthropic-ratelimit-tokens-remaining"),
		LimitTokens:              intHeader(h, "x-ratelimit-limit-tokens", "anthropic-ratelimit-tokens-limit"),
		ResetTokens:              strHeader(h, "x-ratelimit-reset-tokens", "anthropic-ratelimit-tokens-reset"),
		RemainingProjectTokens:   intHeader(h, "x-ratelimit-remaining-project-tokens"),
		LimitProjectTokens:       intHeader(h, "x-ratelimit-limit-project-tokens"),
		ResetProjectTokens:       strHeader(h, "x-ratelimit-reset-project-tokens"),
		RemainingRequestsDay:     intHeader(h, "x-ratelimit-remaining-requests-day"),
		LimitRequestsDay:         intHeader(h, "x-ratelimit-limit-requests-day"),
		ResetRequestsDay:         strHeader(h, "x-ratelimit-reset-requests-day"),
		RemainingRequestsMinute:  intHeader(h, "x-ratelimit-remaining-requests-minute"),
		LimitRequestsMinute:      intHeader(h, "x-ratelimit-limit-requests-minute"),
		ResetRequestsMinute:      strHeader(h, "x-ratelimit-reset-requests-minute"),
		RemainingTokensMinute:    intHeader(h, "x-ratelimit-remaining-tokens-minute"),
		LimitTokensMinute:        intHeader(h, "x-ratelimit-limit-tokens-minute"),
		ResetTokensMinute:        strHeader(h, "x-ratelimit-reset-tokens-minute"),
		RemainingInputTokens:     intHeader(h, "anthropic-ratelimit-input-tokens-remaining"),
		LimitInputTokens:         intHeader(h, "anthropic-ratelimit-input-tokens-limit"),
		RemainingOutputTokens:    intHeader(h, "anthropic-ratelimit-output-tokens-remaining"),
		LimitOutputTokens:        intHeader(h, "anthropic-ratelimit-output-tokens-limit"),
		RemainingTokensPrompt:    intHeader(h, "x-ratelimit-remaining-tokens-prompt"),
		RemainingTokensGenerated: intHeader(h, "x-ratelimit-remaining-tokens-generated"),
		Model:                    strHeader(h, "openai-model"),
	}
	if !s.Reported() {
		return Snapshot{}
	}
	s.CapturedAt = time.Now().UTC()
	return s
}

func intHeader(h http.Header, keys ...string) *int64 {
	for _, key := range keys {
		v := strings.TrimSpace(h.Get(key))
		if v == "" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		out := n
		return &out
	}
	return nil
}

func strHeader(h http.Header, keys ...string) string {
	for _, key := range keys {
		v := strings.TrimSpace(h.Get(key))
		if v != "" {
			return v
		}
	}
	return ""
}
