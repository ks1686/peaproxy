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

// Free reports a verified zero input and output price, and no published
// charge for touching the cache.
//
// This is the coarse listing predicate and answers only what a catalog row can
// answer. A published cache rate that is not zero settles the question: the
// provider bills cached tokens, so a zero input rate does not make a cached
// turn free. An unpublished cache rate is left alone, because refusing every
// deployment whose provider simply does not document cache pricing would
// disable free-only routing rather than protect it.
//
// It still cannot prove that a particular request is free: most providers
// publish no cache rates at all, and whether this request reads the cache is
// not known from a catalog row. Routing that must be certain uses
// Quote().FreeFor with the request's actual usage instead.
func (p Price) Free() bool {
	if !p.Verified || p.Input == nil || p.Output == nil {
		return false
	}
	if *p.Input != 0 || *p.Output != 0 {
		return false
	}
	for _, rate := range []*float64{p.CacheRead, p.CacheWrite} {
		if rate != nil && *rate != 0 {
			return false
		}
	}
	return true
}

// blend weighs a request's two halves when two deployments' rates cross, so
// one is cheap to prompt with and the other is cheap to read. It is the
// usual shape of a conversation -- several times more input than output -- and
// it is stated rather than hidden because a ranking between crossing deployments
// is a judgement, not a fact.
const (
	blendInputWeight  = 3
	blendOutputWeight = 1
)

// NotDearer reports whether a is not worse than b on the rates both publish.
//
// This is the question warmth asks, and it is deliberately the same question
// economy ranking asks, asked through the same comparison. Comparing input
// rates alone answers it wrongly whenever the rates cross: the deployment that
// is cheap to prompt with and expensive to read from looks like the bargain,
// and warmth would then hand the request to it. A cache read is discounted; a
// long answer is not, so the half that cannot be discounted has to count.
func NotDearer(a, b Price) bool {
	if !a.Verified || !b.Verified || a.Input == nil || b.Input == nil {
		return false
	}
	if a.Output == nil || b.Output == nil {
		return *a.Input <= *b.Input
	}
	ai, bi := *a.Input, *b.Input
	ao, bo := *a.Output, *b.Output
	return ai <= bi && ao <= bo
}

// Cheaper reports whether a is the better buy than b.
//
// Unknown prices never win, including as zero: an unpriced deployment is not a
// cheap one, it is an unknown one.
//
// When both rates are published the comparison is not made on input alone. Two
// deployments can cross -- cheap to prompt with, expensive to read from -- and
// picking the lower input rate picks the more expensive answer for every
// conversation that produces an answer worth reading. So a deployment that is
// not worse on either rate wins outright, and a crossed pair is compared on a
// weighted blend. Only when a side publishes no output rate does the comparison
// fall back to the rate that was published.
func Cheaper(a, b Price) bool {
	if a.Input == nil || !a.Verified {
		return false
	}
	if b.Input == nil || !b.Verified {
		return true
	}
	ai, bi := *a.Input, *b.Input
	if a.Output == nil || b.Output == nil {
		return ai < bi
	}
	ao, bo := *a.Output, *b.Output
	switch {
	case ai <= bi && ao <= bo:
		return ai < bi || ao < bo
	case ai >= bi && ao >= bo:
		return false
	}
	return blendInputWeight*ai+blendOutputWeight*ao < blendInputWeight*bi+blendOutputWeight*bo
}
