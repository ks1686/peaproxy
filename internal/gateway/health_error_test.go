package gateway

import (
	"errors"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

func TestHealthErrorTextHidesInvalidGrant(t *testing.T) {
	err := adapter.HTTPError{Status: 400, Body: `{"error":"invalid_grant","error_description":"Refresh token not found or invalid"}`}
	got := healthErrorText(err)
	if got != "sign in again; the saved refresh token was rejected" {
		t.Fatalf("health text %q", got)
	}
	if strings.Contains(got, "invalid_grant") || strings.Contains(got, "Refresh token not found") {
		t.Fatalf("raw grant error leaked: %q", got)
	}
	other := healthErrorText(errors.New("upstream HTTP 500: boom"))
	if !strings.Contains(other, "boom") {
		t.Fatalf("unrelated error was rewritten: %q", other)
	}
}
