package stats

import (
	"math"
	"testing"
)

// О12: энтропия типов — 0 у выдачи из одного типа, 1 бит у двух поровну,
// log2(7) у всех семи поровну.
func TestTypeEntropy(t *testing.T) {
	cases := []struct {
		counts map[string]int
		want   float64
	}{
		{map[string]int{"использует": 4}, 0},
		{map[string]int{"использует": 3, "связано": 3}, 1},
		{map[string]int{"а": 1, "б": 1, "в": 1, "г": 1, "д": 1, "е": 1, "ж": 1}, math.Log2(7)},
		{map[string]int{"использует": 3, "связано": 1}, -(0.75*math.Log2(0.75) + 0.25*math.Log2(0.25))},
	}
	for _, c := range cases {
		if got := typeEntropy(c.counts); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%v: %.6f, ждали %.6f", c.counts, got, c.want)
		}
	}
}
