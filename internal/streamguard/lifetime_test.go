package streamguard

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestBoundCommittedLifetime(t *testing.T) {
	g := New(io.Discard, 1024, time.Millisecond)
	if _, err := g.Write([]byte("data: {\"choices\":[]}\n\n")); err != nil {
		t.Fatal(err)
	}
	ctx, stop := g.Bound(context.Background())
	defer stop()
	time.Sleep(20 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
}

func TestUnboundedSurvivesSlowPrelude(t *testing.T) {
	g := New(io.Discard, 1024, time.Millisecond)
	g.Unbounded()
	ctx, stop := g.Bound(context.Background())
	defer stop()
	time.Sleep(20 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatalf("unbounded attempt was canceled: %v", ctx.Err())
	}
	if _, err := g.Write([]byte("data: {\"choices\":[]}\n\n")); err != nil {
		t.Fatalf("late first event rejected: %v", err)
	}
	if !g.Committed() {
		t.Fatal("late first event did not commit")
	}
}

func TestLeftoverBoundDoesNotFailUnboundedAttempt(t *testing.T) {
	g := New(io.Discard, 1024, 20*time.Millisecond)
	_, _ = g.Bound(context.Background())
	g.Reset()
	g.Unbounded()
	_, stop := g.Bound(context.Background())
	defer stop()

	time.Sleep(50 * time.Millisecond)

	if err := g.Err(); err != nil {
		t.Fatalf("earlier attempt's prelude timer failed the unbounded attempt: %v", err)
	}
}

func TestBoundStopWaitsForTimer(t *testing.T) {
	g := New(io.Discard, 1024, 10*time.Millisecond)
	_, stop := g.Bound(context.Background())
	g.mu.Lock()
	time.Sleep(30 * time.Millisecond)
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		g.mu.Unlock()
		t.Fatal("stop returned while its prelude timer could still touch the guard")
	case <-time.After(20 * time.Millisecond):
	}
	g.mu.Unlock()
	<-stopped

	g.Reset()
	g.Unbounded()
	_, next := g.Bound(context.Background())
	defer next()
	time.Sleep(30 * time.Millisecond)

	if err := g.Err(); err != nil {
		t.Fatalf("stopped attempt's prelude timer failed the unbounded attempt: %v", err)
	}
}

func TestResetRearmsAfterUnbounded(t *testing.T) {
	g := New(io.Discard, 1024, time.Millisecond)
	g.Unbounded()
	g.Reset()
	ctx, stop := g.Bound(context.Background())
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("prelude timeout not re-armed after Reset")
	}
	if g.Err() == nil {
		t.Fatal("expected prelude timeout error")
	}
}
