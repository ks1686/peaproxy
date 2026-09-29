// Package router maps a requested model to an account/adapter and failsover on quota errors.
package router

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

// Policy is a failover strategy. Gateway routing honours these names.
type Policy string

const (
	PolicyFillFirst  Policy = "fill-first"
	PolicyRoundRobin Policy = "round-robin"
	PolicySticky     Policy = "sticky"
	PolicyAdaptive   Policy = "adaptive"
)

// ErrNoAccount means every candidate failed or none matched.
var ErrNoAccount = errors.New("no account available for model")

// CooldownError is returned when every matching account is in the 429/401 skip
// window. Callers should fail closed (no immediate re-hit) and honour RetryAfter.
type CooldownError struct {
	RetryAfter time.Duration
	Err        error
}

func (e CooldownError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("all matching accounts in cooldown: %v", e.Err)
	}
	return "all matching accounts in cooldown"
}

func (e CooldownError) Unwrap() error {
	if e.Err != nil {
		return e.Err
	}
	return ErrNoAccount
}

// maxClientRetryAfter caps the Retry-After sent to clients. The internal
// cooldown still honors the upstream reset; OpenCode waits out Retry-After
// uncapped, so an hour-long quota reset would stall a session silently.
const maxClientRetryAfter = 60

// RetryAfterSeconds returns the Retry-After header value for cooldown errors
// and upstream errors that carried a reset hint.
func RetryAfterSeconds(err error) int {
	var wait time.Duration
	var ce CooldownError
	var he adapter.HTTPError
	switch {
	case errors.As(err, &ce):
		wait = ce.RetryAfter
	case errors.As(err, &he) && he.RetryAfter > 0:
		wait = he.RetryAfter
	default:
		return 0
	}
	return min(max(int(wait.Seconds()), 1), maxClientRetryAfter)
}

// RouteError is a terminal or retryable upstream failure.
type RouteError struct {
	Status    int
	Retryable bool
	Err       error
}

func (e RouteError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("router: status %d", e.Status)
	}
	return e.Err.Error()
}

func (e RouteError) Unwrap() error { return e.Err }

// Candidate is one account that can serve a model ID.
type Candidate struct {
	AccountID string
	Adapter   adapter.Adapter
	Cooldown  bool
}

// Router selects candidates and walks them on 429/quota.
type Router struct {
	Policy     Policy
	Candidates []Candidate
}

// Resolve returns adapters that claim the model, skipping cooled-down accounts.
func (r *Router) Resolve(model string) []Candidate {
	out := make([]Candidate, 0, len(r.Candidates))
	for _, c := range r.Candidates {
		if c.Cooldown {
			continue
		}
		out = append(out, c)
	}
	if model == "" {
		return nil
	}
	return out
}

// Chat tries candidates in order until one succeeds. Retryable status/body errors continue; other errors stop.
func (r *Router) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	cands := r.Resolve(req.Model)
	if len(cands) == 0 {
		return adapter.ChatResponse{}, ErrNoAccount
	}
	var last error
	for _, c := range cands {
		resp, err := c.Adapter.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		last = err
		if Retryable(err) {
			continue
		}
		return adapter.ChatResponse{}, err
	}
	return adapter.ChatResponse{}, fmt.Errorf("%w: %v", ErrNoAccount, last)
}

// CatalogQuery is a convenience for applying hide/expose after merging adapter lists.
func CatalogQuery(hideProviders, hideModels, expose []string, filter catalog.Filter) catalog.Query {
	return catalog.Query{
		Filter:        filter,
		HideProviders: hideProviders,
		HideModels:    hideModels,
		ExposeModels:  expose,
		ForClients:    true,
	}
}
