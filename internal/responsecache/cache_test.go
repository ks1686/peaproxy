package responsecache

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestCacheAccountIsolation(t *testing.T) {
	c := New(1024, 256, time.Minute)
	now := time.Now()
	body := []byte("pong")
	a := Key("a", "ep", "m", "chat", []byte("hi"))
	b := Key("b", "ep", "m", "chat", []byte("hi"))
	if !c.Put(a, body, now) {
		t.Fatal("put")
	}
	if _, ok := c.Get(b, now); ok {
		t.Fatal("account isolation failed")
	}
}

func TestCacheModelRevisionInvalidation(t *testing.T) {
	c := New(1024, 256, time.Minute)
	now := time.Now()
	key := Key("a", "ep", "m", "chat", []byte("hi"))
	if !c.Put(key, []byte("pong"), now) {
		t.Fatal("put")
	}
	c.InvalidatePrefix("a\x00ep\x00m\x00")
	if _, ok := c.Get(key, now); ok {
		t.Fatal("revision was not invalidated")
	}
}

func TestToolsBypassCache(t *testing.T) {
	if Eligible("chat", []byte(`{"tools":[{}]}`), true) {
		t.Fatal("tools were cacheable")
	}
	if Eligible("chat", []byte(`{"messages":[]}`), false) {
		t.Fatal("opt-in was ignored")
	}
}

func TestCacheMemoryBound(t *testing.T) {
	c := New(20, 16, time.Minute)
	now := time.Now()
	if c.Put("big", make([]byte, 32), now) {
		t.Fatal("oversized entry was stored")
	}
	if !c.Put("a", []byte("1234567890"), now) || !c.Put("b", []byte("abcdefghijk"), now) {
		t.Fatal("small puts failed")
	}
	if _, ok := c.Get("a", now); ok {
		t.Fatal("older entry exceeded the memory bound")
	}
}

func TestCancelledWaiterDoesNotCancelPeers(t *testing.T) {
	f := &Flight{}
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	var firstErr error
	go func() {
		defer wg.Done()
		_, firstErr = f.Do(ctx, "k", func(ctx context.Context) ([]byte, error) {
			close(started)
			<-release
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return []byte("ok"), nil
		})
	}()
	<-started
	var second []byte
	var secondErr error
	go func() {
		defer wg.Done()
		second, secondErr = f.Do(context.Background(), "k", func(context.Context) ([]byte, error) {
			t.Error("second waiter started another call")
			return nil, errors.New("duplicate")
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for f.waiters("k") < 2 {
		if time.Now().After(deadline) {
			t.Fatal("second waiter did not join")
		}
		runtime.Gosched()
	}
	cancel()
	close(release)
	wg.Wait()
	if firstErr == nil {
		t.Fatal("cancelled waiter succeeded")
	}
	if secondErr != nil || string(second) != "ok" {
		t.Fatalf("peer result %q err %v", second, secondErr)
	}
}
