package gateway

import "testing"

// Nothing should produce a local assistant by default. The whole point is that
// PeaProxy's own inference stays off unless the user asks, and the failure mode
// that matters is an assistant appearing without being configured.
func TestLocalAssistantIsAbsentUnlessConfigured(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	if a := gw.LocalAssistant(); a != nil {
		t.Fatal("a default gateway produced a local assistant")
	}

	yes := true
	gw.cfg.Optimization.LocalAssistant = &yes
	// Opted in but no endpoint: still nothing, because there is no address, and
	// guessing a port would mean probing the user's machine uninvited.
	if a := gw.LocalAssistant(); a != nil {
		t.Fatal("an opt-in with no endpoint produced an assistant")
	}
}
