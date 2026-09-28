package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

// TestAttemptBudgetAcrossAccounts catches retry loops that multiply the total
// number of upstream calls by the number of accounts.
func TestAttemptBudgetAcrossAccounts(t *testing.T) {
	coordinator := newAttemptCoordinator(3)
	accounts := []string{"a", "b", "c"}
	calls := 0
	_, err := coordinator.run(context.Background(), accounts, false, func(_ context.Context, _ string) (string, error) {
		calls++
		return "", adapter.HTTPError{Status: 429, Body: "quota"}
	})
	if !errors.Is(err, errAttemptBudgetExhausted) {
		t.Fatalf("error = %v, want attempt budget exhaustion", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

// TestAmbiguousSideEffectRequestNotReplayed catches a transport failure after
// a mutation may have reached the provider being retried on another account.
func TestAmbiguousSideEffectRequestNotReplayed(t *testing.T) {
	coordinator := newAttemptCoordinator(3)
	calls := 0
	_, err := coordinator.run(context.Background(), []string{"a", "b"}, true, func(_ context.Context, _ string) (string, error) {
		calls++
		return "", attemptError{Err: adapter.HTTPError{Status: 429, Body: "quota"}, Delivery: deliveryAmbiguous}
	})
	if !errors.Is(err, errAmbiguousDelivery) {
		t.Fatalf("error = %v, want ambiguous delivery", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestAttemptCoordinatorReturnsFirstSuccess(t *testing.T) {
	coordinator := newAttemptCoordinator(3)
	got, err := coordinator.run(context.Background(), []string{"a", "b"}, false, func(_ context.Context, account string) (string, error) {
		if account == "a" {
			return "", adapter.HTTPError{Status: 429, Body: "quota"}
		}
		return "ok", nil
	})
	if err != nil || got != "ok" {
		t.Fatalf("result = %q, error = %v", got, err)
	}
}
