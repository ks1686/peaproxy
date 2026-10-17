package gateway

import (
	"context"
	"errors"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/router"
)

var (
	errAttemptBudgetExhausted = errors.New("request attempt budget exhausted")
	errAmbiguousDelivery      = errors.New("request delivery is ambiguous; refusing replay")
)

type deliveryState uint8

const (
	deliveryUnknown deliveryState = iota
	deliveryAmbiguous
)

// attemptError records whether a failed upstream request may have reached a
// side-effecting endpoint. It is intentionally internal until adapters can
// report delivery certainty from their transports.
type attemptError struct {
	Err      error
	Delivery deliveryState
}

func (e attemptError) Error() string { return e.Err.Error() }
func (e attemptError) Unwrap() error { return e.Err }

type attemptCoordinator struct {
	max  int
	used int
}

func newAttemptCoordinator(max int) attemptCoordinator {
	if max < 1 {
		max = 1
	}
	return attemptCoordinator{max: max}
}

// take records one upstream call against the request-wide budget.
func (c *attemptCoordinator) take() error {
	if c.used >= c.max {
		return errAttemptBudgetExhausted
	}
	c.used++
	return nil
}

// final reports whether candidate i is the last one this request can reach,
// either because it is the last reachable index or the attempt budget is spent.
func (c *attemptCoordinator) final(i, last int) bool {
	return i >= last || c.used >= c.max
}

// lastReachable is the index of the last candidate a stream loop can call.
// A body that failed translation only reaches adapters that implement N.
func lastReachable[N any](cands []instance, translated bool) int {
	if translated {
		return len(cands) - 1
	}
	for i := len(cands) - 1; i >= 0; i-- {
		if _, ok := cands[i].Adapter.(N); ok {
			return i
		}
	}
	return -1
}

// run tries candidate accounts within one request-wide attempt budget. Calls
// that may have reached a side-effecting endpoint are never replayed.
func (c *attemptCoordinator) run(ctx context.Context, accounts []string, sideEffecting bool, call func(context.Context, string) (string, error)) (string, error) {
	var last error
	for _, account := range accounts {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := c.take(); err != nil {
			return "", err
		}
		result, err := call(ctx, account)
		if err == nil {
			return result, nil
		}
		last = err
		var delivery attemptError
		if sideEffecting && errors.As(err, &delivery) && delivery.Delivery == deliveryAmbiguous {
			return "", errAmbiguousDelivery
		}
		if !router.Retryable(err) && !router.Transient(err) {
			return "", err
		}
	}
	if last == nil {
		return "", errAttemptBudgetExhausted
	}
	return "", errAttemptBudgetExhausted
}

// ambiguousDelivery stops a side-effecting call from being sent again when
// the transport cannot prove the provider did not receive it.
func ambiguousDelivery(err error) error {
	if err == nil {
		return nil
	}
	var delivery attemptError
	if errors.As(err, &delivery) && delivery.Delivery == deliveryAmbiguous {
		return errAmbiguousDelivery
	}
	var he adapter.HTTPError
	if errors.As(err, &he) && he.Delivery == adapter.DeliveryUncertain {
		return errAmbiguousDelivery
	}
	return nil
}

// failPolicy says how a loop reads one upstream error.
type failPolicy struct {
	// Protocol enables the protocol-refusal branch. Only non-stream loops and
	// translated tails use it; stream loops take it after the prelude check.
	Protocol bool
	// Replay reports whether the error allows another account. Nil means
	// retryable(err) || router.Transient(err).
	Replay func(error) bool
}

// failState is what one request has learned from its failed attempts.
type failState struct {
	Refused          bool
	AdmissionSkipped bool
	SlowPrelude      bool
}

func (p failPolicy) replay(err error) bool {
	if p.Replay != nil {
		return p.Replay(err)
	}
	return retryable(err) || router.Transient(err)
}

// noteAttempt records one failed attempt and reports whether the loop should
// try the next account. A true result has already recorded a cooldown or a
// protocol refusal.
//
// Order for non-stream loops: protocol refusal, admission, replay.
func (g *Gateway) noteAttempt(err error, account, model string, st *failState, p failPolicy) bool {
	if p.Protocol && protocolRefused(err) {
		g.markModelNotChat(account, model)
		st.Refused = true
		return true
	}
	if admissionRejection(err) {
		st.AdmissionSkipped = true
		return true
	}
	if p.replay(err) {
		g.markCooldown(account, model, err)
		return true
	}
	return false
}

// noteStreamAttempt applies the stream order to one failed attempt. proceed
// means try the next account. A non-nil returned error means return it now.
//
// Order: admission, bytes already written to the client, slow prelude, protocol
// refusal, then failoverOn.
func (g *Gateway) noteStreamAttempt(err error, wrote bool, account, model string, st *failState) (proceed bool, returned error) {
	if admissionRejection(err) {
		st.AdmissionSkipped = true
		return true, nil
	}
	if wrote {
		return false, err
	}
	if slowPrelude(err) {
		st.SlowPrelude = true
		return true, nil
	}
	if protocolRefused(err) {
		g.markModelNotChat(account, model)
		st.Refused = true
		return true, nil
	}
	if failoverOn(err) {
		g.markCooldown(account, model, err)
		return true, nil
	}
	return false, err
}
