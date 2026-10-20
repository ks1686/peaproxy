package server

import (
	"testing"

	"github.com/ks1686/peaproxy/internal/usage"
)

func TestARequestReleasesTheSpendHoldItTook(t *testing.T) {
	s, store := ledgerServer(t, `{"prompt_tokens":1000,"completion_tokens":100}`, 0)
	ceilingOn(t, s, 100)
	postChat(t, s)
	if got := store.Reserved(); got != 0 {
		t.Fatalf("reserved = %v after the request's event was recorded, want 0", got)
	}
	if w := store.SpentInLastDays(30); w.Total == 0 {
		t.Fatal("the request was not recorded, so the test proves nothing about its hold")
	}
}

func TestARefusedRequestLeavesAnInFlightHoldInPlace(t *testing.T) {
	s, store := ledgerServer(t, `{"prompt_tokens":1000,"completion_tokens":100}`, 0)
	if _, ok := store.HoldSpendOwned(&usage.HoldSet{}, 30, func(usage.SpendWindow) bool { return true }, 0.6); !ok {
		t.Fatal("could not take the in-flight hold")
	}
	ceilingOn(t, s, 0.6)
	postChatRefused(t, s)
	if got := store.Reserved(); got != 0.6 {
		t.Fatalf("reserved = %v after a refused request, want the in-flight 0.6 untouched", got)
	}
}
