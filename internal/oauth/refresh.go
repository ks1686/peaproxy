package oauth

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// refreshTimeout bounds a refresh that runs on a detached context. The caller's
// deadline may already have passed, so this is our own clock. It sits under the
// 2 minute response-header timeout the shared HTTP client uses, so a hung token
// endpoint ends as a context error the account can retry, not a transport error
// with a spent token still on disk.
const refreshTimeout = 90 * time.Second

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
//
// fn runs on a context detached from the caller's cancelation and deadline, with
// a timeout of its own. By the time fn returns the provider may already have
// rotated a refresh token, and a caller that gave up part-way must not be able
// to lose it: the token is only in fn's hands. Values are preserved, so
// request-scoped metadata still reaches fn.
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

	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	call.tok, call.err = fn(runCtx)
	cancel()
	close(call.done)

	// The entry is deleted only after fn has committed, so a caller arriving in
	// that window joins this flight instead of starting a second refresh with a
	// token the provider has already rotated.
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

// Ensure makes an account's token usable, refreshing it when it is due. It is
// the whole ensure-a-token path in one place because the order of its steps is
// what keeps a provider grant from being revoked:
//
//   - one refresh per key at a time (Do);
//   - the refresh runs on a context detached from the caller, so a request that
//     ends after the provider answered cannot drop a rotated token;
//   - the token is re-read inside the flight, so a caller that snapshotted a
//     spent refresh token does not spend it a second time;
//   - the new token is committed inside the flight, so a caller that arrives
//     after the flight cannot start a second refresh with the spent one;
//   - commit is held across the generation check, the in-memory store and the
//     persist, so a login landing mid-refresh cannot be overwritten on disk.
//
// refresh receives the token as it stands inside the flight, not a snapshot the
// caller took earlier. fixup, when set, carries the old token's identity
// forward (Extra, account id, email) before the commit.
//
// commit serializes the two writers of an account's token, a login and a
// refresh, and is held across persist. It must not be the adapter's
// request-path mutex: a login is rare, and holding a hot-path lock across a
// secret-store write would stall every request on that account.
func Ensure(
	ctx context.Context,
	g *RefreshGroup,
	key string,
	commit *sync.Mutex,
	mu *sync.Mutex,
	token *Token,
	generation *uint64,
	skew time.Duration,
	refresh func(context.Context, Token) (Token, error),
	fixup func(old, next Token) Token,
	persist func(Token) error,
) error {
	if g == nil {
		g = &DefaultRefresh
	}
	_, err := g.Do(ctx, key, func(ctx context.Context) (Token, error) {
		commit.Lock()
		defer commit.Unlock()

		mu.Lock()
		cur := *token
		seen := *generation
		mu.Unlock()

		if !cur.NeedsRefresh(skew) {
			// The flight before us committed a fresh token while this caller
			// was snapshotting the old one. Refreshing again would present a
			// refresh token the provider has already rotated, which is what
			// gets a grant revoked.
			return cur, nil
		}

		next, rerr := refresh(ctx, cur)
		if rerr != nil {
			return Token{}, rerr
		}
		if fixup != nil {
			next = fixup(cur, next)
		}

		mu.Lock()
		kept, store, _ := KeepIfCurrent(seen, *generation, *token, next, nil)
		if !store {
			// A newer login landed. It persisted its own token; this refresh is
			// spent and must not overwrite it.
			mu.Unlock()
			return kept, nil
		}
		*token = kept
		p := persist
		mu.Unlock()

		if p == nil {
			return kept, nil
		}
		// A persist failure does not roll the token back: the refresh token it
		// replaced is already spent, and the caller can still use this one.
		if perr := p(kept); perr != nil {
			return kept, fmt.Errorf("oauth: token refreshed but not saved: %w", perr)
		}
		return kept, nil
	})
	return err
}
