package config

import "testing"

// A suggestion the author follows must actually be the field they meant. A
// wrong suggestion is worse than none: it corrects the spelling and leaves the
// setting just as broken, and now it looks right.

func TestAnEquallyCloseFieldProducesNoSuggestion(t *testing.T) {
	fields := map[string]int{"maxAttempts": 1, "maxInFlight": 1, "cacheResponses": 1}
	// "maxRounds" scores identically against maxAttempts and maxInFlight.
	if got := nearestField(fields, "maxRounds"); got != "" {
		t.Fatalf("suggested %q from a tie; following it would leave the config broken", got)
	}
}

func TestAUnambiguousTypoStillGetsItsSuggestion(t *testing.T) {
	fields := map[string]int{"maxAttempts": 1, "cacheResponses": 1}
	if got := nearestField(fields, "cacheResponse"); got != "cacheResponses" {
		t.Fatalf("suggested %q, want cacheResponses", got)
	}
	if got := nearestField(map[string]int{"baseURL": 1, "maxAttempts": 1}, "baseUrl"); got != "baseURL" {
		t.Fatalf("suggested %q, want baseURL", got)
	}
}

// Nothing is close enough, so nothing is claimed.
func TestAnUnrelatedKeyGetsNoSuggestion(t *testing.T) {
	fields := map[string]int{"maxAttempts": 1, "promptCache": 1}
	if got := nearestField(fields, "zzqqxx"); got != "" {
		t.Fatalf("suggested %q for a key resembling nothing", got)
	}
}
