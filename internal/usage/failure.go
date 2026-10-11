package usage

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

// FailureClass says what went wrong, as opposed to how a retry should be
// handled. It is a separate axis from router.FailoverClass, which answers "cool
// this account and try another?" -- a question a log cannot use to tell someone
// what to do. Both of them call a 401 "auth", but only one of those means the
// user has to log in again, and the other means the secret store lost a value.
// The pattern overlap with router is deliberate: two taxonomies that answer
// different questions beat one that answers both badly.
type FailureClass string

const (
	// FailureNone is a request that succeeded cleanly. Events carry the empty
	// class rather than a "success" value, so adding a class never has to
	// rewrite history.
	FailureNone FailureClass = ""

	// The three below are recorded but are not faults. They stay in the error
	// store because they are worth seeing -- a spike in cancellations is an
	// agent crashing mid-stream, and an unmeasured call is the state a spend
	// ceiling fails closed on -- but counting them as failures would bury the
	// failures they sit next to.
	ClassClientCancel  FailureClass = "client_cancel"
	ClassNotDispatched FailureClass = "not_dispatched"
	ClassUnmeasured    FailureClass = "unmeasured"

	ClassAuthRevoked       FailureClass = "auth_revoked"
	ClassAuthRefreshFailed FailureClass = "auth_refresh_failed"
	ClassSecretUnreadable  FailureClass = "secret_unreadable"
	ClassRateLimit         FailureClass = "rate_limit"
	ClassEntitlement       FailureClass = "entitlement"
	ClassOverloaded        FailureClass = "overloaded"
	ClassUpstreamError     FailureClass = "upstream_error"
	ClassNotFound          FailureClass = "not_found"
	ClassStreamCut         FailureClass = "stream_cut"
	ClassTimeout           FailureClass = "timeout"
	ClassUnknown           FailureClass = "unknown"
)

// IsFailure reports whether this class is a fault. The three non-failure classes
// are retained in the error store and counted separately on export.
func (c FailureClass) IsFailure() bool {
	switch c {
	case FailureNone, ClassClientCancel, ClassNotDispatched, ClassUnmeasured:
		return false
	default:
		return true
	}
}

// ClassifyFailure names a failed request. status is the HTTP status PeaProxy
// returned or received, and may be 0 when the failure happened before a status
// existed.
//
// Order matters. The body patterns are consulted before the status because a
// provider that quotes "quota exceeded" at a 400 has said something more useful
// than "400", and a 400 that quotes "invalid_grant" is an auth failure wearing
// a status that does not admit it.
func ClassifyFailure(status int, err error) FailureClass {
	if err == nil && status < http.StatusBadRequest {
		return FailureNone
	}
	msg := ""
	if err != nil {
		msg = err.Error()
		// A cancelled caller is the most common "error" in this file and is
		// not a fault. Checked before anything else so no pattern below can
		// claim it.
		if isCancellation(err) {
			return ClassClientCancel
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return ClassTimeout
		}
	}
	lower := strings.ToLower(msg)

	if containsAny(lower, secretUnreadablePatterns) {
		return ClassSecretUnreadable
	}
	// After secret-unreadable, and before the generic auth patterns, because a
	// refresh that was refused says more than the status it arrived wrapped in.
	// It deliberately does not match invalid_grant on its own: a bare
	// invalid_grant does not prove a refresh was attempted, and claiming one
	// was would report a user-actionable refresh failure for a plain revoked
	// grant.
	if containsAny(lower, refreshFailedPatterns) {
		return ClassAuthRefreshFailed
	}
	if status == http.StatusUnauthorized || containsAny(lower, authPatterns) {
		return ClassAuthRevoked
	}
	if status == http.StatusTooManyRequests || containsAny(lower, rateLimitPatterns) {
		return ClassRateLimit
	}
	if status == http.StatusPaymentRequired || containsAny(lower, entitlementPatterns) {
		return ClassEntitlement
	}
	if status == http.StatusServiceUnavailable || status == 529 || containsAny(lower, overloadedPatterns) {
		return ClassOverloaded
	}
	if status == http.StatusNotFound {
		return ClassNotFound
	}
	if containsAny(lower, timeoutPatterns) {
		return ClassTimeout
	}
	// A status means an upstream said something and we could not say more about
	// it. No status at all means the failure happened before or outside HTTP --
	// a transport error, a serialization bug -- and attributing that to the
	// provider would be a guess, so it stays unknown.
	if status >= http.StatusBadRequest {
		return ClassUpstreamError
	}
	if err != nil {
		return ClassUnknown
	}
	return FailureNone
}

// ClassifyStream names a stream that ended without its terminal event. It is
// separate from ClassifyFailure because the request "succeeded": the client
// holds an answer that stops mid-sentence, which is what it can only report as
// a missing finish_reason.
func ClassifyStream(terminal string, known bool) FailureClass {
	switch {
	case terminal != "":
		return FailureNone
	case known:
		// Published counts, but nothing said the stream was over.
		return ClassStreamCut
	default:
		// No terminal event and no counts: unmeasured as well as cut.
		return ClassUnmeasured
	}
}

func isCancellation(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	// The server wraps the cause in an HTTP error, so the sentinel is not
	// always reachable through errors.Is.
	return strings.Contains(strings.ToLower(err.Error()), "context canceled")
}

func containsAny(lower string, pats []string) bool {
	for _, p := range pats {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

var secretUnreadablePatterns = []string{
	"is unreadable",
	"secretstore:",
	"treating it as absent",
}

var refreshFailedPatterns = []string{
	"oauth: token",
	"oauth: refresh",
	"token endpoint",
	"refresh token",
	"refresh_token",
}

var authPatterns = []string{
	"unauthorized",
	"unauthenticated",
	"not_authenticated",
	"authentication_error",
	"invalid_api_key",
	"invalid api key",
	"token expired",
	"expired_token",
	"expired token",
	"invalid_grant",
	"401",
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
	"429",
}

var entitlementPatterns = []string{
	"freetiererror",
	"free tier",
	"insufficient funds",
	"insufficient_funds",
	"payment_required",
	"payment required",
	"402",
}

var overloadedPatterns = []string{
	"overloaded",
	"high demand",
	"capacity exceeded",
	"temporarily unavailable",
	"unavailable_error",
	"try again later",
}

var timeoutPatterns = []string{
	"deadline exceeded",
	"timeout",
	"timed out",
}

// errorRetention is how long the error store keeps a record. Time-based rather
// than size-based on purpose: an outage produces thousands of failures in
// minutes, and a size cap would evict the evidence of the failure that caused it
// while keeping the consequences.
const errorRetention = 7 * 24 * time.Hour

// ErrorRetention is the window errors.jsonl keeps, exported for the admin
// surface and the export command.
func ErrorRetention() time.Duration { return errorRetention }

// ErrorCutoff is the oldest event time the store still keeps.
func ErrorCutoff(now time.Time, window time.Duration) time.Time {
	return now.UTC().Add(-window)
}

// ErrorStoreName is the file failures are written to, next to the request log.
const ErrorStoreName = "errors.jsonl"

// ErrorLogPath is the error store for a config directory.
func ErrorLogPath(dir string) string { return dir + string(os.PathSeparator) + ErrorStoreName }
