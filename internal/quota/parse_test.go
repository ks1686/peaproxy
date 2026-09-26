package quota

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestParseOpenAIRateLimitHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-limit-requests", "60")
	h.Set("x-ratelimit-remaining-requests", "59")
	h.Set("x-ratelimit-reset-requests", "1s")
	h.Set("x-ratelimit-limit-tokens", "150000")
	h.Set("x-ratelimit-remaining-tokens", "149984")
	h.Set("x-ratelimit-reset-tokens", "6m0s")
	h.Set("openai-model", "gpt-4o")
	s := ParseHeaders(h)
	if !s.Reported() || *s.RemainingRequests != 59 || *s.RemainingTokens != 149984 {
		t.Fatalf("%#v", s)
	}
	if s.Model != "gpt-4o" || s.ResetRequests != "1s" || s.Source != SourceHeaders {
		t.Fatalf("%#v", s)
	}
}

func TestParseExplicitZeroIsKept(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "0")
	s := ParseHeaders(h)
	if s.RemainingRequests == nil || *s.RemainingRequests != 0 {
		t.Fatalf("want honest 0, got %#v", s)
	}
}

func TestParseMissingHeadersAreUnknownNotZero(t *testing.T) {
	s := ParseHeaders(http.Header{"Content-Type": []string{"application/json"}})
	if s.Reported() || s.RemainingRequests != nil || s.RemainingTokens != nil {
		t.Fatalf("unknown must not be 0: %#v", s)
	}
	b, err := json.Marshal(Snapshot{AccountID: "local", Adapter: "ollama", Source: SourceNone, Note: "not reported by provider"})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, bad := range []string{`"remainingRequests"`, `"remainingTokens"`, `"remainingCredits"`, `"creditsUnlimited"`} {
		if strings.Contains(got, bad) {
			t.Fatalf("unknown leaked %s in %s", bad, got)
		}
	}
}

func TestParseJSONKeepsExplicitZero(t *testing.T) {
	z := int64(0)
	b, err := json.Marshal(Snapshot{RemainingRequests: &z})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"remainingRequests":0`) {
		t.Fatalf("%s", b)
	}
}

func TestParseAnthropicHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-requests-remaining", "48")
	h.Set("anthropic-ratelimit-requests-limit", "50")
	h.Set("anthropic-ratelimit-requests-reset", "2026-09-26T20:00:00Z")
	h.Set("anthropic-ratelimit-tokens-remaining", "8000")
	h.Set("anthropic-ratelimit-input-tokens-remaining", "3000")
	h.Set("anthropic-ratelimit-output-tokens-remaining", "5000")
	s := ParseHeaders(h)
	if s.RemainingRequests == nil || *s.RemainingRequests != 48 {
		t.Fatalf("%#v", s)
	}
	if s.RemainingInputTokens == nil || *s.RemainingInputTokens != 3000 {
		t.Fatalf("%#v", s)
	}
}

func TestParseCerebrasAndSambaNovaWindows(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests-day", "12")
	h.Set("x-ratelimit-limit-requests-day", "20")
	h.Set("x-ratelimit-remaining-tokens-minute", "9000")
	h.Set("x-ratelimit-remaining-requests-minute", "18")
	s := ParseHeaders(h)
	if s.RemainingRequestsDay == nil || *s.RemainingRequestsDay != 12 {
		t.Fatalf("%#v", s)
	}
	if s.RemainingTokensMinute == nil || *s.RemainingTokensMinute != 9000 {
		t.Fatalf("%#v", s)
	}
	if s.RemainingRequestsMinute == nil || *s.RemainingRequestsMinute != 18 {
		t.Fatalf("%#v", s)
	}
}

func TestParseIgnoresNonIntegerRemaining(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "plenty")
	s := ParseHeaders(h)
	if s.Reported() {
		t.Fatalf("garbage must not become a number: %#v", s)
	}
}

func TestParseDoesNotInventUnlimitedFromMissingHeaders(t *testing.T) {
	s := ParseHeaders(http.Header{})
	if s.CreditsUnlimited {
		t.Fatal("missing headers must not mean unlimited")
	}
}
