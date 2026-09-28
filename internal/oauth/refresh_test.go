package oauth

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRefreshSingleFlight(t *testing.T) {
	var g RefreshGroup
	var calls atomic.Int32
	inFn := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := g.Do(context.Background(), "acct", func(context.Context) (Token, error) {
			calls.Add(1)
			close(inFn)
			<-release
			return Token{AccessToken: "new"}, nil
		})
		errCh <- err
	}()
	<-inFn
	go func() {
		defer wg.Done()
		_, err := g.Do(context.Background(), "acct", func(context.Context) (Token, error) {
			calls.Add(1)
			return Token{AccessToken: "other"}, nil
		})
		errCh <- err
	}()
	for g.waiters("acct") == 0 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
}

func TestLateRefreshCannotOverwriteLogin(t *testing.T) {
	current := Token{AccessToken: "login"}
	next := Token{AccessToken: "stale-refresh"}
	got, stored, err := KeepIfCurrent(1, 2, current, next, nil)
	if err != nil || stored {
		t.Fatalf("stored=%v err=%v", stored, err)
	}
	if got.AccessToken != "login" {
		t.Fatalf("token %q", got.AccessToken)
	}
}

func TestRefreshPersistenceFailureKeepsNewestRuntimeToken(t *testing.T) {
	persistErr := errors.New("disk full")
	next := Token{AccessToken: "rotated", RefreshToken: "new-rt"}
	got, stored, err := KeepIfCurrent(3, 3, Token{AccessToken: "old"}, next, persistErr)
	if !stored || !errors.Is(err, persistErr) {
		t.Fatalf("stored=%v err=%v", stored, err)
	}
	if got.RefreshToken != "new-rt" {
		t.Fatalf("rolled back to %q", got.RefreshToken)
	}
}
