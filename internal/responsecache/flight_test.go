package responsecache

import (
	"context"
	"sync"
	"testing"
	"time"
)

type flightKey struct{}

// The shared call runs on behalf of the leader's request, so request-scoped
// values (the spend-hold collector among them) must reach it, while the
// leader's own cancellation must not stop it for the others.
func TestFlightRunsWithTheLeadersRequestValues(t *testing.T) {
	var f Flight
	ctx := context.WithValue(context.Background(), flightKey{}, "leader")
	var seen any
	if _, _, err := f.DoShared(ctx, "k", func(run context.Context) ([]byte, error) {
		seen = run.Value(flightKey{})
		return []byte("ok"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != "leader" {
		t.Fatalf("flight fn saw value %v, want the leader's request values", seen)
	}
}

func TestFlightReportsWhichCallerLedTheCall(t *testing.T) {
	var f Flight
	release := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	var leaderShared, followerShared bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, leaderShared, _ = f.DoShared(context.Background(), "k", func(context.Context) ([]byte, error) {
			close(started)
			<-release
			return []byte("body"), nil
		})
	}()
	<-started
	done := make(chan struct{})
	go func() {
		_, followerShared, _ = f.DoShared(context.Background(), "k", func(context.Context) ([]byte, error) {
			t.Error("a follower must not run its own call")
			return nil, nil
		})
		close(done)
	}()
	for f.waiters("k") < 2 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	<-done
	if leaderShared {
		t.Fatal("the caller that ran the call is reported as sharing another's result")
	}
	if !followerShared {
		t.Fatal("the caller that joined is not reported as sharing the leader's result")
	}
}

// Once every waiter has abandoned a call it is cancelled; a caller arriving
// before its goroutine exits must start a new call, not inherit the
// cancellation.
func TestFlightJoinerDoesNotInheritAnAbandonedCall(t *testing.T) {
	var f Flight
	block := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := f.Do(ctx, "k", func(run context.Context) ([]byte, error) {
			<-run.Done()
			<-block
			return nil, run.Err()
		})
		errc <- err
	}()
	for f.waiters("k") < 1 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-errc
	type result struct {
		body []byte
		err  error
	}
	got := make(chan result, 1)
	go func() {
		body, err := f.Do(context.Background(), "k", func(context.Context) ([]byte, error) {
			return []byte("fresh"), nil
		})
		got <- result{body, err}
	}()
	defer close(block)
	select {
	case r := <-got:
		if r.err != nil || string(r.body) != "fresh" {
			t.Fatalf("joiner after abandonment got %q, %v; want a fresh call", r.body, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("joiner after abandonment joined the cancelled call instead of starting a fresh one")
	}
}
