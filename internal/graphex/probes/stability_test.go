package probes

import "testing"

// TestJaccard — сходство двух наборов написаний, формула |A∩B|/|A∪B|.
func TestJaccard(t *testing.T) {
	cases := []struct {
		name string
		a, b map[string]bool
		want float64
	}{
		{"оба пустых", map[string]bool{}, map[string]bool{}, 1},
		{"один пустой", map[string]bool{"x": true}, map[string]bool{}, 0},
		{"полное совпадение", map[string]bool{"x": true, "y": true}, map[string]bool{"x": true, "y": true}, 1},
		{"частичное пересечение", map[string]bool{"x": true, "y": true}, map[string]bool{"y": true, "z": true}, 1.0 / 3},
	}
	const eps = 1e-9
	for _, c := range cases {
		got := jaccard(c.a, c.b)
		diff := got - c.want
		if diff < 0 {
			diff = -diff
		}
		if diff > eps {
			t.Errorf("%s: jaccard = %v, хочу %v", c.name, got, c.want)
		}
	}
}

// TestNewIn — сколько ключей a нет в b.
func TestNewIn(t *testing.T) {
	a := map[string]bool{"x": true, "y": true, "z": true}
	b := map[string]bool{"y": true}
	if got := newIn(a, b); got != 2 {
		t.Fatalf("newIn = %d, хочу 2", got)
	}
	if got := newIn(b, a); got != 0 {
		t.Fatalf("newIn = %d, хочу 0", got)
	}
}

// TestThinChunksKeepsAll — если кандидатов не больше n, список не режется.
func TestThinChunksKeepsAll(t *testing.T) {
	in := []int{1, 2, 3}
	out := thinChunks(in, 5)
	if len(out) != 3 {
		t.Fatalf("len = %d, хочу 3", len(out))
	}
}

// TestThinChunksSamplesEvenly — при избытке кандидатов выборка равномерна
// по всему списку (шаг len/n), а не первые n подряд.
func TestThinChunksSamplesEvenly(t *testing.T) {
	in := make([]int, 100)
	for i := range in {
		in[i] = i
	}
	out := thinChunks(in, 10)
	if len(out) == 0 || len(out) > 10 {
		t.Fatalf("len(out) = %d, хочу 1..10", len(out))
	}
	// Равномерность: индексы возрастают с шагом len(in)/n = 10, не с шагом 1.
	if len(out) >= 2 && out[1]-out[0] != len(in)/10 {
		t.Fatalf("шаг выборки = %d, хочу %d", out[1]-out[0], len(in)/10)
	}
}
