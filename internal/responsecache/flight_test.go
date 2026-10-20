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

// A shared upstream call is billed once: exactly one caller that received the
// result is told it was not shared, whichever of them that is.
func TestFlightBillsExactlyOneReceiver(t *testing.T) {
	var f Flight
	release := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	var leaderShared, followerShared bool
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, leaderShared, _ = f.DoShared(context.Background(), "k", func(context.Context) ([]byte, error) {
			close(started)
			<-release
			return []byte("body"), nil
		})
	}()
	<-started
	go func() {
		defer wg.Done()
		_, followerShared, _ = f.DoShared(context.Background(), "k", func(context.Context) ([]byte, error) {
			t.Error("a follower must not run its own call")
			return nil, nil
		})
	}()
	for f.waiters("k") < 2 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	if leaderShared == followerShared {
		t.Fatalf("leader shared=%v follower shared=%v: exactly one receiver must bill the call", leaderShared, followerShared)
	}
}

// The caller that started the call can leave (a disconnect, its own deadline)
// while another keeps waiting. The upstream call still finishes and is billed,
// so the receiver that gets it must not be told it was shared.
func TestFlightBillsTheFollowerWhenTheLeaderLeaves(t *testing.T) {
	var f Flight
	release := make(chan struct{})
	started := make(chan struct{})
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := f.DoShared(leaderCtx, "k", func(context.Context) ([]byte, error) {
			close(started)
			<-release
			return []byte("body"), nil
		})
		leaderDone <- err
	}()
	<-started
	type result struct {
		shared bool
		err    error
	}
	followerDone := make(chan result, 1)
	go func() {
		_, shared, err := f.DoShared(context.Background(), "k", func(context.Context) ([]byte, error) {
			return nil, nil
		})
		followerDone <- result{shared, err}
	}()
	for f.waiters("k") < 2 {
		time.Sleep(time.Millisecond)
	}
	cancelLeader()
	if err := <-leaderDone; err == nil {
		t.Fatal("the leader left but got a result")
	}
	close(release)
	r := <-followerDone
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.shared {
		t.Fatal("the only receiver of a finished upstream call was told it was shared, so nobody bills it")
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
