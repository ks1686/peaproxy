package catalog

import (
	"time"

	"github.com/ks1686/peaproxy/internal/economics"
)

// Price is a verified per-million-token quote. Nil amounts are unknown, not free.
type Price struct {
	Input  *float64
	Output *float64
	// CacheRead and CacheWrite are what the provider publishes for cached
	// tokens. Most providers publish neither, which leaves them unknown: a
	// request that reads or writes the cache may then cost more than the
	// input and output rates suggest.
	CacheRead  *float64
	CacheWrite *float64
	Currency   string
	Source     string
	ObservedAt time.Time
	Verified   bool
}

// Quote converts a catalog row into the shared pricing type.
//
// The conversion is deliberately one-way and total: an unpublished component
// stays nil rather than defaulting to zero, so the routing layer can tell
// "free" from "not published".
func (p Price) Quote() economics.Quote {
	return economics.Quote{
		Currency:   p.Currency,
		Input:      p.Input,
		Output:     p.Output,
		CacheRead:  p.CacheRead,
		CacheWrite: p.CacheWrite,
		Source:     p.Source,
		ObservedAt: p.ObservedAt,
		Verified:   p.Verified,
	}
}

// Free reports a verified zero input and output price.
//
// This is the coarse listing predicate and answers only what a catalog row can
// answer. It cannot prove that a particular request is free: a cached turn may
// cost more than these two rates show, and most providers publish no cache
// rates at all. Routing that must be certain uses Quote().FreeFor with the
// request's actual usage instead.
func (p Price) Free() bool {
	if !p.Verified || p.Input == nil || p.Output == nil {
		return false
	}
	return *p.Input == 0 && *p.Output == 0
}

// Cheaper reports whether a is a known lower input price than b.
// Unknown prices never win, including as zero.
func Cheaper(a, b Price) bool {
	if a.Input == nil || !a.Verified {
		return false
	}
	if b.Input == nil || !b.Verified {
		return true
	}
	return *a.Input < *b.Input
}
