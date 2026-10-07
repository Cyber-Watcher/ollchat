package stats

import "testing"

// Медиана замера троек (tripleEval): по определению, выборка не портится,
// у пустой медианы нет — и печатается прочерк, а не ноль.
func TestMedian(t *testing.T) {
	cases := []struct {
		xs   []float64
		want float64
	}{
		{[]float64{0.7}, 0.7},
		{[]float64{0.9, 0.5, 0.7}, 0.7},
		{[]float64{0.8, 0.6, 0.5, 0.9}, 0.7},
		{nil, 0},
	}
	for _, c := range cases {
		if got := median(c.xs); got < c.want-1e-12 || got > c.want+1e-12 {
			t.Errorf("median(%v) = %v, ожидалось %v", c.xs, got, c.want)
		}
	}
	xs := []float64{0.9, 0.1, 0.5}
	median(xs)
	if xs[0] != 0.9 || xs[1] != 0.1 || xs[2] != 0.5 {
		t.Errorf("медиана переставила саму выборку: %v", xs)
	}
	if got := medianText(nil); got != "—" {
		t.Errorf("медиана пустой выборки печатается %q, ожидался прочерк", got)
	}
	if got := medianText([]float64{0.6123, 0.7}); got != "0.656" {
		t.Errorf("medianText = %q", got)
	}
}
