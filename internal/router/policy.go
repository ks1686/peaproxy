package router

import (
	"sort"
	"strings"
	"time"
)

// NormalizePolicy maps config/docs names onto a known policy.
// Empty and unknown values are round-robin (the runtime default).
func NormalizePolicy(p Policy) Policy {
	switch Policy(strings.ToLower(strings.TrimSpace(string(p)))) {
	case PolicyFillFirst:
		return PolicyFillFirst
	case PolicySticky:
		return PolicySticky
	case PolicyAdaptive:
		return PolicyAdaptive
	default:
		return PolicyRoundRobin
	}
}

// AccountStat is measured routing state. Unknown latency does not outrank a measured account.
type AccountStat struct {
	InFlight int
	Latency  time.Duration
	Errors   float64
	Known    bool
}

// AdaptiveOrder prefers lower in-flight count, then lower error rate, then lower latency.
// Accounts with no measurements stay in their existing relative order after measured ones.
func AdaptiveOrder(hot []string, stats map[string]AccountStat) []string {
	if len(hot) == 0 {
		return nil
	}
	out := append([]string(nil), hot...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := stats[out[i]], stats[out[j]]
		if a.Known != b.Known {
			return a.Known
		}
		if !a.Known {
			return false
		}
		if a.InFlight != b.InFlight {
			return a.InFlight < b.InFlight
		}
		if a.Errors != b.Errors {
			return a.Errors < b.Errors
		}
		return a.Latency < b.Latency
	})
	return out
}

// Order returns hot account IDs in try-order for a policy.
// hot must already exclude cooled-down accounts and stay in config order.
// Round-robin rotates the start index via rr. Sticky puts sticky first when
// that account is still hot; with no memory it behaves like fill-first.
func Order(policy Policy, hot []string, rr *uint64, sticky string) []string {
	if len(hot) == 0 {
		return nil
	}
	switch NormalizePolicy(policy) {
	case PolicyFillFirst:
		return append([]string(nil), hot...)
	case PolicySticky:
		return stickyOrder(hot, sticky)
	case PolicyAdaptive:
		return append([]string(nil), hot...)
	case PolicyRoundRobin:
		if rr == nil {
			return append([]string(nil), hot...)
		}
		start := int(*rr % uint64(len(hot)))
		*rr++
		out := make([]string, 0, len(hot))
		out = append(out, hot[start:]...)
		out = append(out, hot[:start]...)
		return out
	default:
		return Order(PolicyRoundRobin, hot, rr, sticky)
	}
}

func stickyOrder(hot []string, sticky string) []string {
	if sticky == "" {
		return append([]string(nil), hot...)
	}
	rest := make([]string, 0, len(hot))
	found := false
	for _, id := range hot {
		if id == sticky {
			found = true
			continue
		}
		rest = append(rest, id)
	}
	if !found {
		return rest
	}
	out := make([]string, 0, len(hot))
	out = append(out, sticky)
	out = append(out, rest...)
	return out
}
