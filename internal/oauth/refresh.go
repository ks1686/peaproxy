package oauth

import (
	"context"
	"sync"
)

// RefreshGroup runs one refresh at a time for each account key.
type RefreshGroup struct {
	mu       sync.Mutex
	inflight map[string]*refreshCall
}

type refreshCall struct {
	done    chan struct{}
	waiters int
	tok     Token
	err     error
}

// DefaultRefresh is the process-wide refresh flight used by OAuth adapters.
var DefaultRefresh RefreshGroup

// Do runs fn once per key. Later callers wait for that result or for ctx.
func (g *RefreshGroup) Do(ctx context.Context, key string, fn func(context.Context) (Token, error)) (Token, error) {
	if err := ctx.Err(); err != nil {
		return Token{}, err
	}
	g.mu.Lock()
	if g.inflight == nil {
		g.inflight = map[string]*refreshCall{}
	}
	if call, ok := g.inflight[key]; ok {
		call.waiters++
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return Token{}, ctx.Err()
		case <-call.done:
			return call.tok, call.err
		}
	}
	call := &refreshCall{done: make(chan struct{})}
	g.inflight[key] = call
	g.mu.Unlock()

	call.tok, call.err = fn(ctx)
	close(call.done)

	g.mu.Lock()
	delete(g.inflight, key)
	g.mu.Unlock()
	return call.tok, call.err
}

func (g *RefreshGroup) waiters(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	call := g.inflight[key]
	if call == nil {
		return 0
	}
	return call.waiters
}

// KeepIfCurrent stores next when no newer login landed while refresh was in flight.
// A persistence failure still keeps next, because the previous refresh token may already be spent.
func KeepIfCurrent(seen, latest uint64, current, next Token, persistErr error) (Token, bool, error) {
	if latest != seen {
		return current, false, nil
	}
	return next, true, persistErr
}

// CommitRefresh stores next when no newer login landed. A persist error does not roll the token back.
func CommitRefresh(mu *sync.Mutex, token *Token, generation *uint64, seen uint64, next Token, persist func(Token) error) error {
	mu.Lock()
	kept, store, _ := KeepIfCurrent(seen, *generation, *token, next, nil)
	if store && *generation == seen {
		*token = kept
	} else {
		store = false
	}
	mu.Unlock()
	if !store || persist == nil {
		return nil
	}
	return persist(kept)
}
