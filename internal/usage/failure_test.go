package usage

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A failure that cannot be told apart from another failure cannot be counted.
// These are the strings PeaProxy actually produces, one case per class.
func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		name   string
		status int
		err    error
		want   FailureClass
	}{
		{name: "no error at all", want: FailureNone},
		{name: "client hung up", err: errors.New("context canceled"), want: ClassClientCancel},
		{name: "client hung up with cause", err: errors.New(`Post "http://x": context canceled`), want: ClassClientCancel},
		{name: "deadline", err: context.DeadlineExceeded, want: ClassTimeout},
		{name: "unauthorized", status: http.StatusUnauthorized, err: errors.New("upstream HTTP 401"), want: ClassAuthRevoked},
		{name: "invalid grant", status: http.StatusBadRequest, err: errors.New(`{"error":"invalid_grant"}`), want: ClassAuthRevoked},
		{name: "refresh refused", err: errors.New("oauth: token endpoint: invalid_grant"), want: ClassAuthRefreshFailed},
		{name: "secret unreadable", err: errors.New(`stored oauth secret for account "a" is unreadable`), want: ClassSecretUnreadable},
		{name: "rate limited", status: http.StatusTooManyRequests, err: errors.New("HTTP 429"), want: ClassRateLimit},
		{name: "quota body without a 429", status: http.StatusBadRequest, err: errors.New(`{"error":{"message":"quota exceeded"}}`), want: ClassRateLimit},
		{name: "payment required", status: http.StatusPaymentRequired, err: errors.New("HTTP 402"), want: ClassEntitlement},
		{name: "free tier refusal", status: http.StatusForbidden, err: errors.New("FreeTierError: insufficient funds"), want: ClassEntitlement},
		{name: "overloaded", status: http.StatusServiceUnavailable, err: errors.New("HTTP 503"), want: ClassOverloaded},
		{name: "upstream error", status: http.StatusBadRequest, err: errors.New("HTTP 400"), want: ClassUpstreamError},
		{name: "not found", status: http.StatusNotFound, err: errors.New("HTTP 404"), want: ClassNotFound},
		{name: "nothing recognised", err: errors.New("something odd happened"), want: ClassUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyFailure(c.status, c.err); got != c.want {
				t.Fatalf("ClassifyFailure(%d, %v) = %q, want %q", c.status, c.err, got, c.want)
			}
		})
	}
}

// A secret that cannot be read and a grant the provider refused are different
// problems with different fixes, and "log in again" is only correct for one of
// them. The one that is not has to stop being reported as the other.
func TestSecretUnreadableIsNotReportedAsAuthRevoked(t *testing.T) {
	err := errors.New(`stored oauth secret for account "anthropic-oauth" is unreadable; treating it as absent (log in again)`)
	if got := ClassifyFailure(0, err); got != ClassSecretUnreadable {
		t.Fatalf("got %q, want %q -- telling the user to log in again when the store lost the secret sends them to the wrong place", got, ClassSecretUnreadable)
	}
}

// FailoverAuth in the router classifies both of these as auth-expired and cools
// the account for both. Diagnosis is a different axis: one needs a login, the
// other needs a fix to the secret store, and only one of them is the user's to
// act on.
func TestAuthClassesAreDistinctFromEachOther(t *testing.T) {
	if ClassAuthRevoked == ClassAuthRefreshFailed {
		t.Fatal("auth_revoked and auth_refresh_failed must be distinguishable")
	}
}

// The three non-failure classes have to stay non-failures or the error stream
// fills with the noise it exists to exclude.
func TestNonFailureClasses(t *testing.T) {
	for _, c := range []FailureClass{ClassClientCancel, ClassNotDispatched, ClassUnmeasured} {
		if c.IsFailure() {
			t.Fatalf("%q is reported as a failure", c)
		}
	}
	for _, c := range []FailureClass{ClassAuthRevoked, ClassAuthRefreshFailed, ClassSecretUnreadable, ClassRateLimit, ClassUnknown} {
		if !c.IsFailure() {
			t.Fatalf("%q is not reported as a failure", c)
		}
	}
}

// A stream that ended with no terminal event is its own failure: the client
// sees a response that simply stops.
func TestStreamCutIsItsOwnClass(t *testing.T) {
	if got := ClassStreamCut; got.IsFailure() != true || got == ClassUnknown {
		t.Fatalf("stream_cut is classified as %q (failure=%v)", got, got.IsFailure())
	}
}

// Redact has to survive a token arriving inside a provider's error body, which
// is exactly the case that matters here: a 401 that echoes the credential is the
// most useful line in the file and the one that must never be uploaded.
func TestRedactRemovesATokenFromAProviderErrorBody(t *testing.T) {
	body := `{"error":{"message":"invalid token: ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"}}`
	got := Redact(body)
	if strings.Contains(got, "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
		t.Fatalf("a token survived redaction: %s", got)
	}
}

// A cancellation with no error attached is still a cancellation. recordEvent
// calls this on the path where a client disconnects cleanly.
func TestCancellationWithoutAnErrorIsStillACancel(t *testing.T) {
	if got := ClassifyFailure(0, nil); got != FailureNone {
		t.Fatalf("a request with no error was classified %q", got)
	}
}

// The cutoff is what makes retention time-based rather than size-based, so it
// has to be a pure function of the event time and the window.
func TestErrorCutoff(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	cut := ErrorCutoff(now, 7*24*time.Hour)
	if !cut.Equal(now.AddDate(0, 0, -7)) {
		t.Fatalf("cutoff = %v, want %v", cut, now.AddDate(0, 0, -7))
	}
	inside := now.AddDate(0, 0, -6)
	if inside.Before(cut) {
		t.Fatalf("an event %v inside the window was cut", inside)
	}
	outside := now.AddDate(0, 0, -8)
	if !outside.Before(cut) {
		t.Fatalf("an event %v outside the window survived", outside)
	}
}

// The Copilot adapter stores a github_token, and a Copilot 401 quotes the
// offending token in prose rather than in a labelled field. That makes this the
// most likely credential on this machine to reach a file we intend to upload.
func TestRedactRemovesGitHubTokensInEveryForm(t *testing.T) {
	for _, tok := range []string{
		"ghu_ExampleTokenNotARealCredential0",
		"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789",
		"gho_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789",
		"github_pat_11ABCDEFG0aBcDeFgHiJkL_MnOpQrStUvWxYz0123456789",
	} {
		for _, body := range []string{
			`{"error":{"message":"invalid token: ` + tok + `"}}`,
			"bad credentials: " + tok,
			`{"error":"` + tok + `"}`,
		} {
			if got := Redact(body); strings.Contains(got, tok) {
				t.Fatalf("token %s survived redaction: %s", tok, got)
			}
		}
	}
}

// Redaction must not eat the words around the secret: the class is read from
// the same string, and a file of "[redacted]" tells nobody anything.
func TestRedactKeepsTheSurroundingMessage(t *testing.T) {
	got := Redact(`{"error":{"message":"invalid token: ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"}}`)
	if !strings.Contains(got, "invalid token") {
		t.Fatalf("redaction destroyed the diagnostic: %s", got)
	}
}
