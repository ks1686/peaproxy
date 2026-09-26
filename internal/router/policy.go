package router

import "strings"

// NormalizePolicy maps config/docs names onto a known policy.
// Empty and unknown values are round-robin (the runtime default).
func NormalizePolicy(p Policy) Policy {
	switch Policy(strings.ToLower(strings.TrimSpace(string(p)))) {
	case PolicyFillFirst:
		return PolicyFillFirst
	case PolicySticky:
		return PolicySticky
	default:
		return PolicyRoundRobin
	}
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
