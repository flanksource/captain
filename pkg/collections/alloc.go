package collections

import "math"

// SafeAdd returns a + b, saturating at math.MaxInt if the sum would overflow.
//
// It is intended for computing make() size or capacity hints from independent
// lengths (e.g. len(a)+len(b)) without risking an integer-overflow wraparound,
// which could otherwise yield a tiny allocation followed by out-of-bounds
// writes. Negative operands are treated as an overflow guard as well.
func SafeAdd(a, b int) int {
	if a < 0 || b < 0 || a > math.MaxInt-b {
		return math.MaxInt
	}
	return a + b
}
