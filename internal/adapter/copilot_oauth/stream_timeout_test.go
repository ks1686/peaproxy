package copilot_oauth

import (
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

// http.Client.Timeout bounds reading the body too, so a 30s client cut off
// every Copilot Responses stream or long completion at 30s.
func TestCopilotResponsesWireHasNoTotalTimeout(t *testing.T) {
	a, err := New(adapter.Options{ID: "copilot"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(*Adapter).wireClient().Timeout; got != 0 {
		t.Fatalf("Responses wire client timeout = %v, want 0 so streams are not cut off", got)
	}
}
