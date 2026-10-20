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
