package ollama

import (
	"testing"
	"time"
)

// TestBackoff — пауза удваивается, случайная добавка в пределах 0…0,5 с, потолок 30 с
// (этап 112, А2).
func TestBackoff(t *testing.T) {
	for _, c := range []struct {
		attempt int
		lo      time.Duration
	}{{0, time.Second}, {1, 2 * time.Second}, {2, 4 * time.Second}, {10, 30 * time.Second}, {-1, time.Second}} {
		seen := map[time.Duration]bool{}
		for range 50 {
			d := Backoff(c.attempt, time.Second)
			if d < c.lo || d >= c.lo+500*time.Millisecond {
				t.Fatalf("попытка %d: пауза %v вне [%v, %v)", c.attempt, d, c.lo, c.lo+500*time.Millisecond)
			}
			seen[d] = true
		}
		if len(seen) < 2 {
			t.Fatalf("попытка %d: 50 пауз одинаковы — случайной добавки нет", c.attempt)
		}
	}
}
