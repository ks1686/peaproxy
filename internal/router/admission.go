package router

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrAdmissionQueue is returned when an account's wait queue is full.
var ErrAdmissionQueue = errors.New("account admission queue is full")

// ErrAdmissionWait is returned when a queued request exceeds the wait limit.
var ErrAdmissionWait = errors.New("account admission wait exceeded")

// Gate limits in-flight work per account. A zero max is unlimited.
//
// MaxInFlight, MaxQueue and Wait are read under mu by Acquire, so they must be
// written with SetLimits rather than assigned directly. They used to be
// assigned by the gateway on every request, which raced every reader (#74).
type Gate struct {
	MaxInFlight int
	MaxQueue    int
	Wait        time.Duration

	mu       sync.Mutex
	inflight map[string]int
	queued   map[string]int
	probe    map[string]time.Time
	// wake is closed and replaced whenever a slot is released, so waiters
	// re-check immediately instead of polling on a timer (#80).
	wake chan struct{}
}

// SetLimits publishes the configured limits. Called when the gateway is built
// or rebuilt, never per request.
func (g *Gate) SetLimits(maxInFlight, maxQueue int, wait time.Duration) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.MaxInFlight = maxInFlight
	g.MaxQueue = maxQueue
	g.Wait = wait
	g.mu.Unlock()
}

// limits returns the configured limits under the lock.
func (g *Gate) limits() (maxInFlight, maxQueue int, wait time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.MaxInFlight, g.MaxQueue, g.Wait
}

// wakeWaiters closes the broadcast channel. Callers hold mu.
func (g *Gate) wakeWaiters() {
	if g.wake != nil {
		close(g.wake)
	}
	g.wake = make(chan struct{})
}

// Acquire reserves a slot. The returned function releases it once.
func (g *Gate) Acquire(ctx context.Context, account string) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	max, maxQueue, wait := g.limits()
	if max <= 0 {
		return func() {}, nil
	}
	if maxQueue <= 0 {
		maxQueue = 32
	}
	if wait <= 0 {
		wait = 2 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		g.mu.Lock()
		if g.inflight == nil {
			g.inflight = map[string]int{}
			g.queued = map[string]int{}
		}
		if g.inflight[account] < max {
			g.inflight[account]++
			g.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() { g.release(account) })
			}, nil
		}
		if g.queued[account] >= maxQueue {
			g.mu.Unlock()
			return nil, ErrAdmissionQueue
		}
		g.queued[account]++
		// One long wait, woken by release, instead of a 5ms poll that allocated
		// a timer per waiter per spin and woke every waiter every 5ms whether or
		// not anything had changed (#80).
		wake := g.wake
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			g.leaveQueue(account)
			return nil, ctx.Err()
		case <-timer.C:
			g.leaveQueue(account)
			return nil, ErrAdmissionWait
		case <-wake:
			g.leaveQueue(account)
		}
	}
}

func (g *Gate) leaveQueue(account string) {
	g.mu.Lock()
	if g.queued[account] > 0 {
		g.queued[account]--
	}
	g.mu.Unlock()
}

func (g *Gate) release(account string) {
	g.mu.Lock()
	if g.inflight[account] > 0 {
		g.inflight[account]--
	}
	delete(g.probe, account)
	// A released slot is the only thing a blocked waiter is waiting for.
	g.wakeWaiters()
	g.mu.Unlock()
}

// TryHalfOpen lets a single caller probe an account that just left cooldown.
// A probe that is never closed expires so the account cannot stay dark.
func (g *Gate) TryHalfOpen(account string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.probe == nil {
		g.probe = map[string]time.Time{}
	}
	if at, ok := g.probe[account]; ok && time.Since(at) < 30*time.Second {
		return false
	}
	g.probe[account] = time.Now()
	return true
}

// EndHalfOpen releases a probe that did not become an in-flight slot.
func (g *Gate) EndHalfOpen(account string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.probe, account)
	g.mu.Unlock()
}
