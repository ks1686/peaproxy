package sizehint

import (
	"math"
	"testing"
)

func TestSum(t *testing.T) {
	if got := Sum(1, 2, 3); got != 6 {
		t.Fatalf("Sum = %d", got)
	}
	if got := Sum(); got != 0 {
		t.Fatalf("empty Sum = %d", got)
	}
	if got := Sum(math.MaxInt, 1); got != 0 {
		t.Fatalf("overflow Sum = %d", got)
	}
	if got := Sum(-1, 4); got != 0 {
		t.Fatalf("negative Sum = %d", got)
	}
	if got := Sum(math.MaxInt); got != math.MaxInt {
		t.Fatalf("MaxInt Sum = %d", got)
	}
}

func TestMul(t *testing.T) {
	if got := Mul(4, 5); got != 20 {
		t.Fatalf("Mul = %d", got)
	}
	if got := Mul(0, 5); got != 0 {
		t.Fatalf("zero Mul = %d", got)
	}
	if got := Mul(-2, 3); got != 0 {
		t.Fatalf("negative Mul = %d", got)
	}
	if got := Mul(math.MaxInt, 2); got != 0 {
		t.Fatalf("overflow Mul = %d", got)
	}
	if got := Mul(math.MaxInt, 1); got != math.MaxInt {
		t.Fatalf("MaxInt Mul = %d", got)
	}
}
