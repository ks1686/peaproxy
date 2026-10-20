package usage

import (
	"context"
	"sync"
)

// HoldSet collects the spend holds one request takes, so the event that
// records the request releases exactly those. A request that fails over takes
// one hold per attempt and records one event; a request that is refused, served
// from cache or run locally takes none and must release none.
type HoldSet struct {
	mu  sync.Mutex
	ids []uint64
}

type holdsKey struct{}

// WithHolds attaches a HoldSet to ctx unless one is already there.
func WithHolds(ctx context.Context) context.Context {
	if HoldsFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, holdsKey{}, &HoldSet{})
}

// WithOwnHolds gives ctx a fresh HoldSet when it is a tracked request, even if
// one is already there. A coalesced call uses it so its holds are its own and
// can be handed to whichever receiver is billed. Outside a tracked request it
// returns ctx unchanged.
func WithOwnHolds(ctx context.Context) context.Context {
	if HoldsFrom(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, holdsKey{}, &HoldSet{})
}

// Absorb adds holds taken under another set to this one. On a nil set it
// reports false and the caller has to release the ids itself.
func (h *HoldSet) Absorb(ids []uint64) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	h.ids = append(h.ids, ids...)
	h.mu.Unlock()
	return true
}

// HoldsFrom returns the request's HoldSet, or nil outside a tracked request.
func HoldsFrom(ctx context.Context) *HoldSet {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(holdsKey{}).(*HoldSet)
	return h
}

func (h *HoldSet) add(id uint64) {
	h.mu.Lock()
	h.ids = append(h.ids, id)
	h.mu.Unlock()
}

// Take returns the hold ids collected so far and clears the set.
func (h *HoldSet) Take() []uint64 {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := h.ids
	h.ids = nil
	return ids
}
