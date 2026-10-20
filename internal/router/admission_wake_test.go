package router

import (
	"context"
	"testing"
	"time"
)

// On a fresh gate the broadcast channel did not exist until the first release,
// so a request queued before that captured a nil channel, was never woken, and
// timed out although the slot came free almost at once.
func TestFirstWaiterOnAFreshGateIsWokenByTheFirstRelease(t *testing.T) {
	g := &Gate{}
	g.SetLimits(1, 0, time.Second)
	release, err := g.Acquire(context.Background(), "acct")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		release()
	}()
	start := time.Now()
	second, err := g.Acquire(context.Background(), "acct")
	if err != nil {
		t.Fatalf("queued request failed after %v: %v; the slot was released at 50ms", time.Since(start), err)
	}
	second()
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Fatalf("queued request waited %v for a slot released at 50ms", waited)
	}
}
