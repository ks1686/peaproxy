package router

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

func TestClassifyStatusCodes(t *testing.T) {
	cases := []struct {
		status int
		want   FailoverClass
	}{
		{http.StatusTooManyRequests, FailoverRateLimit},
		{http.StatusUnauthorized, FailoverAuth},
		{http.StatusServiceUnavailable, FailoverOverloaded},
		{529, FailoverOverloaded},
		{http.StatusBadRequest, FailoverNone},
		{http.StatusNotFound, FailoverNone},
		{http.StatusInternalServerError, FailoverNone},
	}
	for _, tc := range cases {
		got := Classify(adapter.HTTPError{Status: tc.status, Body: "nope"})
		if got != tc.want {
			t.Fatalf("status %d: got %s want %s", tc.status, got, tc.want)
		}
	}
}

func TestClassifyErrorBodies(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   FailoverClass
	}{
		{"anthropic rate limit json", 400, `{"type":"error","error":{"type":"rate_limit_error","message":"quota"}}`, FailoverRateLimit},
		{"openai insufficient quota", 403, `{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`, FailoverRateLimit},
		{"google resource exhausted", 400, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded"}}`, FailoverRateLimit},
		{"anthropic overloaded json", 500, `{"error":{"type":"overloaded_error","message":"Overloaded"}}`, FailoverOverloaded},
		{"overloaded phrase on 500", 500, `{"error":{"message":"The model is overloaded, try again later"}}`, FailoverOverloaded},
		{"auth expired json", 403, `{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`, FailoverAuth},
		{"token expired phrase", 400, `{"error":"access token expired"}`, FailoverAuth},
		{"usage limit phrase on 400", 400, `{"error":{"message":"You have reached your usage limit","type":"invalid_request_error"}}`, FailoverRateLimit},
		{"out of usage phrase on 400", 400, `{"error":{"message":"out of usage for this model"}}`, FailoverRateLimit},
		{"plain 400 stays terminal", 400, `{"error":{"type":"invalid_request_error","message":"messages must be an array"}}`, FailoverNone},
		{"zen free tier", 403, `{"type":"error","error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode"}}`, FailoverEntitlement},
		{"payment required", 402, `{"error":{"message":"insufficient funds"}}`, FailoverEntitlement},
		{"plain 403 stays terminal", 403, `{"error":{"message":"forbidden"}}`, FailoverNone},
		{"model not found stays terminal", 404, `{"error":{"code":"model_not_found"}}`, FailoverNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(adapter.HTTPError{Status: tc.status, Body: tc.body})
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestClassifyReasonOmitsSecrets(t *testing.T) {
	err := adapter.HTTPError{
		Status: 403,
		Body:   `{"error":{"type":"authentication_error","api_key":"sk-secret-live","access_token":"tok-secret"}}`,
	}
	got := Classify(err)
	if got != FailoverAuth {
		t.Fatalf("got %s", got)
	}
	reason := got.String()
	if strings.Contains(reason, "sk-secret") || strings.Contains(reason, "tok-secret") || strings.Contains(reason, "api_key") {
		t.Fatalf("reason leaked secret: %q", reason)
	}
	if !Retryable(err) {
		t.Fatal("auth-expired body must be retryable")
	}
	if Retryable(adapter.HTTPError{Status: 400, Body: "bad json"}) {
		t.Fatal("plain 400 must not be retryable")
	}
	if Retryable(errors.New("network down")) {
		t.Fatal("plain errors are not retryable")
	}
}
