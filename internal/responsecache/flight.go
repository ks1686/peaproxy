package responsecache

import (
	"context"
	"sync"
)

// Flight coalesces eligible callers. One cancelled waiter does not cancel the rest.
type Flight struct {
	mu    sync.Mutex
	calls map[string]*call
}

type call struct {
	done    chan struct{}
	waiters int
	cancel  context.CancelFunc
	body    []byte
	err     error
	// billed is set by the first caller to collect the result, which records
	// the upstream cost; every later receiver records a shared hit.
	billed bool
}

// Do runs fn once per key.
func (f *Flight) Do(ctx context.Context, key string, fn func(context.Context) ([]byte, error)) ([]byte, error) {
	body, _, err := f.DoShared(ctx, key, fn)
	return body, err
}

// DoShared is Do, also reporting whether this caller's result was shared, i.e.
// another receiver of the same upstream call is the one billed for it. Exactly
// one receiver is told it was not shared: the first to collect the result, which
// is normally the caller that started the call but is a follower when that
// caller left before the call finished. Without that, a call whose leader
// disconnected would finish upstream and be billed to nobody.
//
// fn runs with the leader's request values (the spend-hold collector among
// them) but not its cancellation: one caller leaving must not cancel the call
// for the rest.
func (f *Flight) DoShared(ctx context.Context, key string, fn func(context.Context) ([]byte, error)) ([]byte, bool, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]*call{}
	}
	if existing, ok := f.calls[key]; ok {
		existing.waiters++
		f.mu.Unlock()
		return f.wait(ctx, key, existing)
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c := &call{done: make(chan struct{}), waiters: 1, cancel: cancel}
	f.calls[key] = c
	f.mu.Unlock()
	go func() {
		c.body, c.err = fn(runCtx)
		close(c.done)
		f.mu.Lock()
		f.forgetLocked(key, c)
		f.mu.Unlock()
	}()
	return f.wait(ctx, key, c)
}

// forgetLocked removes c from the map if it is still the call for key.
func (f *Flight) forgetLocked(key string, c *call) {
	if f.calls[key] == c {
		delete(f.calls, key)
	}
}

func (f *Flight) waiters(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil || f.calls[key] == nil {
		return 0
	}
	return f.calls[key].waiters
}

func (f *Flight) wait(ctx context.Context, key string, c *call) ([]byte, bool, error) {
	select {
	case <-ctx.Done():
		f.abandon(key, c)
		return nil, true, ctx.Err()
	case <-c.done:
	}
	// select picks at random when the caller is already cancelled and the
	// shared call finishes in the same instant. A cancelled waiter must not
	// observe success, and must not cancel peers who are still waiting.
	if err := ctx.Err(); err != nil {
		f.abandon(key, c)
		return nil, true, err
	}
	f.mu.Lock()
	c.waiters--
	shared := c.billed
	c.billed = true
	f.mu.Unlock()
	if c.err != nil {
		return nil, shared, c.err
	}
	return append([]byte(nil), c.body...), shared, nil
}

func (f *Flight) abandon(key string, c *call) {
	f.mu.Lock()
	c.waiters--
	last := c.waiters == 0
	if last {
		// A cancelled call must not be joined by a caller arriving before its
		// goroutine exits; that caller would inherit the cancellation.
		f.forgetLocked(key, c)
	}
	f.mu.Unlock()
	if last {
		c.cancel()
	}
}
