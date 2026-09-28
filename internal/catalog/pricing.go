package catalog

// Price is a verified per-million-token quote. Nil amounts are unknown, not free.
type Price struct {
	Input    *float64
	Output   *float64
	Currency string
	Verified bool
}

// Free reports a verified zero input and output price.
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
