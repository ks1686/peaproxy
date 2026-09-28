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
