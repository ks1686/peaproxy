package gateway

import (
	"time"
)

// cooldownSlots is one account's cooling state: at most one account-wide
// cooldown, plus one per model.
//
// It used to be a single Cooldown per account, so a model-scoped failure
// replaced whatever was stored -- including a cooldown for the whole account,
// or for a different model. A 401 that cooled the account was undone by a 429
// on another model, and the first model became eligible again (#73).
//
// A narrower cooldown never replaces a wider one: cooling one model says
// nothing about the account's health elsewhere, so it must not reach outside
// the model it names.
type cooldownSlots struct {
	wide   Cooldown
	models map[string]Cooldown
}

// set records a cooldown. An empty model is the account-wide one.
func (s *cooldownSlots) set(model string, c Cooldown) {
	if model == "" {
		// A repeated account-wide failure only ever extends it.
		if !s.wideActive() || c.Until.After(s.wide.Until) {
			s.wide = c
		}
		return
	}
	if s.models == nil {
		s.models = map[string]Cooldown{}
	}
	// A model-scoped cooldown is about one model; it never touches the
	// account-wide entry.
	s.models[model] = c
}

// forModel returns the cooldown that applies to a request for model: the
// account-wide one if there is one, otherwise the entry for that model.
func (s cooldownSlots) forModel(model string) (Cooldown, bool) {
	if s.wideActive() {
		return s.wide, true
	}
	c, ok := s.models[model]
	return c, ok
}

func (s cooldownSlots) wideActive() bool {
	return s.wide.AccountID != "" && time.Now().Before(s.wide.Until)
}

// anyActive reports whether any slot is still cooling, which is what health and
// the automatic-route ranking care about.
func (s cooldownSlots) anyActive(now time.Time) (Cooldown, bool) {
	if now.Before(s.wide.Until) && s.wide.AccountID != "" {
		return s.wide, true
	}
	var soonest Cooldown
	found := false
	for _, c := range s.models {
		if !now.Before(c.Until) {
			continue
		}
		if !found || c.Until.Before(soonest.Until) {
			soonest, found = c, true
		}
	}
	return soonest, found
}

// prune drops expired entries so the map does not grow with every model a
// request ever failed on.
func (s *cooldownSlots) prune(now time.Time) {
	if s.wide.AccountID != "" && !now.Before(s.wide.Until) {
		s.wide = Cooldown{}
	}
	for model, c := range s.models {
		if !now.Before(c.Until) {
			delete(s.models, model)
		}
	}
}

// activeCooldown is the one place that answers "is this account out for this
// request", so every caller agrees.
func activeCooldown(slots cooldownSlots, model string, now time.Time) (Cooldown, bool) {
	if c, ok := slots.forModel(model); ok && now.Before(c.Until) {
		return c, true
	}
	return Cooldown{}, false
}

// all returns every active cooldown for an account, account-wide first. The
// Health UI lists them so a person can see which model is out and why, rather
// than one arbitrary entry.
func (s cooldownSlots) all(now time.Time) []Cooldown {
	var out []Cooldown
	if now.Before(s.wide.Until) && s.wide.AccountID != "" {
		out = append(out, s.wide)
	}
	for _, c := range s.models {
		if now.Before(c.Until) {
			out = append(out, c)
		}
	}
	return out
}

// coolingOtherModel reports whether the account has an active cooldown for some
// model other than the one asked for. Such an account is fine for this request,
// so it is not a reason to report a cooldown -- which it would be under the old
// single slot, where the account-wide entry it may also hold was all there was.
func (s cooldownSlots) coolingOtherModel(model string, now time.Time) bool {
	for m, c := range s.models {
		if m != model && now.Before(c.Until) {
			return true
		}
	}
	return false
}
