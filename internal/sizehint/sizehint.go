// Package sizehint adds slice-capacity hints without wrapping int.
// A hint that would overflow is 0, and append grows the slice instead.
package sizehint

import "math"

// Sum returns the sum of non-negative parts.
// It returns 0 when any part is negative or the sum would overflow int.
func Sum(parts ...int) int {
	n := 0
	for _, p := range parts {
		if p < 0 || n > math.MaxInt-p {
			return 0
		}
		n += p
	}
	return n
}

// Mul returns a*b when both are non-negative and the product fits in int.
// Otherwise it returns 0.
func Mul(a, b int) int {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > math.MaxInt/b {
		return 0
	}
	return a * b
}
