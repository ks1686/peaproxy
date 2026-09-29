package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/router"
)

func TestCooldownErrorNamesRecoveringAccounts(t *testing.T) {
	// Given expired cooldowns whose half-open probes are already occupied.
	a, b := 0, 0
	gw := twoAccountGateway(t, countOK(&a, "a"), countOK(&b, "b"))
	for _, id := range []string{"acct-a", "acct-b"} {
		gw.cool[id] = Cooldown{AccountID: id, Until: time.Now().Add(-time.Millisecond)}
		if !gw.admission.TryHalfOpen(id) {
			t.Fatalf("could not reserve probe for %s", id)
		}
	}

	// When the request has no available account.
	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))

	// Then the cooldown error explains which accounts are recovering.
	var cooled router.CooldownError
	if !errors.As(err, &cooled) || cooled.RetryAfter <= 0 {
		t.Fatalf("error=%v, want cooldown with retry delay", err)
	}
	for _, text := range []string{"acct-a", "acct-b", "recovering"} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("cooldown error %q does not contain %q", err, text)
		}
	}
	if a != 0 || b != 0 {
		t.Fatalf("recovering accounts were called: a=%d b=%d", a, b)
	}
}
