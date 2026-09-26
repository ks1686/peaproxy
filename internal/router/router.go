// Package router maps a requested model to an account/adapter and failsover on quota errors.
package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

// Policy is a failover strategy. Implementations in the spike will do real rotation.
type Policy string

const (
	PolicyFillFirst  Policy = "fill-first"
	PolicyRoundRobin Policy = "round-robin"
	PolicySticky     Policy = "sticky"
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

// RetryAfterSeconds returns the Retry-After header value for cooldown errors.
func RetryAfterSeconds(err error) int {
	var ce CooldownError
	if !errors.As(err, &ce) {
		return 0
	}
	sec := int(ce.RetryAfter.Seconds())
	if sec < 1 {
		return 1
	}
	return sec
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

// Chat tries candidates in order until one succeeds. Quota/429 continues; other errors stop.
//
// TODO(spike): inspect provider error bodies, not only HTTP status; persist cooldowns.
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
		if retryable(err) {
			continue
		}
		return adapter.ChatResponse{}, err
	}
	return adapter.ChatResponse{}, fmt.Errorf("%w: %v", ErrNoAccount, last)
}

func retryable(err error) bool {
	var re RouteError
	if errors.As(err, &re) {
		return re.Retryable || re.Status == http.StatusTooManyRequests
	}
	var he adapter.HTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusTooManyRequests || he.Status == http.StatusUnauthorized || he.Status == http.StatusServiceUnavailable
	}
	return false
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
