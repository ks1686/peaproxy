package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ks1686/peaproxy/internal/adapter"
)

// FailoverClass is why an upstream error should skip to the next account.
// Values are safe to put in cooldown reasons (no provider bodies).
type FailoverClass string

const (
	FailoverNone        FailoverClass = ""
	FailoverRateLimit   FailoverClass = "rate-limit"
	FailoverOverloaded  FailoverClass = "overloaded"
	FailoverAuth        FailoverClass = "auth-expired"
	FailoverEntitlement FailoverClass = "entitlement"
)

func (c FailoverClass) String() string {
	if c == FailoverNone {
		return "none"
	}
	return string(c)
}

// Retryable reports whether the gateway should cool the account and try the next.
func Retryable(err error) bool {
	return Classify(err) != FailoverNone
}

// Classify inspects HTTP status and, when safe, provider error bodies.
// It never returns body text — only a coarse class.
func Classify(err error) FailoverClass {
	if err == nil {
		return FailoverNone
	}
	var re RouteError
	if errors.As(err, &re) {
		if c := classifyStatus(re.Status); c != FailoverNone {
			return c
		}
		if re.Retryable {
			return FailoverRateLimit
		}
		if re.Err != nil {
			if c := Classify(re.Err); c != FailoverNone {
				return c
			}
		}
		return FailoverNone
	}
	var he adapter.HTTPError
	if errors.As(err, &he) {
		if c := classifyStatus(he.Status); c != FailoverNone {
			return c
		}
		return classifyBody(he.Body)
	}
	return FailoverNone
}

// Transient reports a 502, a 504, or a 503 whose body is an edge proxy's
// transport failure (connect error, reset before headers) rather than the
// provider saying it is overloaded. The gateway retries that account once
// before cooling it. 403 is not transient and does not fail over.
func Transient(err error) bool {
	var he adapter.HTTPError
	if !errors.As(err, &he) {
		return false
	}
	switch he.Status {
	case http.StatusBadGateway, http.StatusGatewayTimeout:
		return true
	case http.StatusServiceUnavailable:
		return containsAny(strings.ToLower(he.Body), edgeTransportPatterns)
	default:
		return false
	}
}

var edgeTransportPatterns = []string{
	"upstream connect error",
	"reset before headers",
	"connection refused",
	"connection timeout",
}

func classifyStatus(status int) FailoverClass {
	switch status {
	case http.StatusTooManyRequests:
		return FailoverRateLimit
	case http.StatusUnauthorized:
		return FailoverAuth
	case http.StatusPaymentRequired:
		return FailoverEntitlement
	case http.StatusServiceUnavailable, 529:
		return FailoverOverloaded
	default:
		return FailoverNone
	}
}

func classifyBody(body string) FailoverClass {
	body = strings.TrimSpace(body)
	if body == "" {
		return FailoverNone
	}
	var raw any
	if json.Unmarshal([]byte(body), &raw) == nil {
		if c := classifyJSON(raw, 0); c != FailoverNone {
			return c
		}
	}
	return classifyText(body)
}

func classifyJSON(v any, depth int) FailoverClass {
	if depth > 6 {
		return FailoverNone
	}
	switch x := v.(type) {
	case map[string]any:
		for _, key := range []string{"type", "code", "status", "message", "error"} {
			if child, ok := x[key]; ok {
				if c := classifyJSON(child, depth+1); c != FailoverNone {
					return c
				}
			}
		}
	case string:
		return classifyText(x)
	case float64:
		return classifyStatus(int(x))
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return FailoverNone
		}
		return classifyStatus(int(n))
	case []any:
		for _, child := range x {
			if c := classifyJSON(child, depth+1); c != FailoverNone {
				return c
			}
		}
	}
	return FailoverNone
}

func classifyText(s string) FailoverClass {
	lower := strings.ToLower(s)
	switch {
	case containsAny(lower, rateLimitPatterns):
		return FailoverRateLimit
	case containsAny(lower, overloadedPatterns):
		return FailoverOverloaded
	case containsAny(lower, authPatterns):
		return FailoverAuth
	case containsAny(lower, entitlementPatterns):
		return FailoverEntitlement
	default:
		return FailoverNone
	}
}

var rateLimitPatterns = []string{
	"rate_limit",
	"ratelimit",
	"rate limit",
	"too many requests",
	"resource_exhausted",
	"insufficient_quota",
	"quota exceeded",
	"quota_exceeded",
	"usage_limit",
	"tokens_exceeded",
	"billing_hard_limit",
}

var overloadedPatterns = []string{
	"overloaded",
	"high demand",
	"capacity exceeded",
	"temporarily unavailable",
	"unavailable_error",
	"try again later",
}

var entitlementPatterns = []string{
	"freetiererror",
	"free tier",
	"insufficient funds",
	"insufficient_funds",
	"payment_required",
	"payment required",
}

var authPatterns = []string{
	"authentication_error",
	"invalid_api_key",
	"invalid api key",
	"invalid x-api-key",
	"unauthorized",
	"unauthenticated",
	"not_authenticated",
	"token expired",
	"expired_token",
	"expired token",
	"invalid_grant",
	"auth_error",
}

func containsAny(lower string, pats []string) bool {
	for _, p := range pats {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
