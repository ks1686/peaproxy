package router

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAdmissionCancelReleasesLease(t *testing.T) {
	g := &Gate{MaxInFlight: 1, MaxQueue: 2, Wait: time.Second}
	release, err := g.Acquire(context.Background(), "acct")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Acquire(ctx, "acct"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	release()
	release2, err := g.Acquire(context.Background(), "acct")
	if err != nil {
		t.Fatal(err)
	}
	release2()
}

func TestHalfOpenSingleProbe(t *testing.T) {
	g := &Gate{}
	if !g.TryHalfOpen("acct") {
		t.Fatal("first probe was refused")
	}
	if g.TryHalfOpen("acct") {
		t.Fatal("second probe was allowed")
	}
	g.EndHalfOpen("acct")
	if !g.TryHalfOpen("acct") {
		t.Fatal("probe did not reset")
	}
}

func TestAdmissionQueueBound(t *testing.T) {
	g := &Gate{MaxInFlight: 1, MaxQueue: 1, Wait: 30 * time.Millisecond}
	hold, err := g.Acquire(context.Background(), "acct")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = g.Acquire(context.Background(), "acct")
	}()
	time.Sleep(10 * time.Millisecond)
	_, err = g.Acquire(context.Background(), "acct")
	if !errors.Is(err, ErrAdmissionQueue) && !errors.Is(err, ErrAdmissionWait) {
		t.Fatalf("error = %v", err)
	}
	wg.Wait()
}
