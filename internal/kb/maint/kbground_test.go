package maint

import (
	"math"
	"testing"
)

// Нарезка ответа на предложения для О11: по концу фразы и по строкам,
// ограды кода и обрывки короче 15 букв не берутся, предел соблюдается.
func TestAnswerSentences(t *testing.T) {
	answer := "## Ответ\n" +
		"Команда EXPLAIN показывает план запроса. Вариант ANALYZE ещё и выполняет его!\n" +
		"```sql\n" +
		"EXPLAIN SELECT 1;\n" +
		"```\n" +
		"- пункт\n" +
		"Версия 1.2 вышла давно, и это важно помнить"
	got := answerSentences(answer, 10)
	want := []string{
		"Команда EXPLAIN показывает план запроса.",
		"Вариант ANALYZE ещё и выполняет его!",
		"Версия 1.2 вышла давно, и это важно помнить",
	}
	if len(got) != len(want) {
		t.Fatalf("предложений %d, ждали %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("предложение %d: %q, ждали %q", i, got[i], want[i])
		}
	}
	if got := answerSentences(answer, 1); len(got) != 1 {
		t.Errorf("предел 1, получено %d", len(got))
	}
}

func TestWithGold(t *testing.T) {
	got := withGold("books/1#1", []string{"books/2#2", "books/1#1", "books/3#3"}, 3)
	want := []string{"books/1#1", "books/2#2", "books/3#3"}
	if len(got) != len(want) {
		t.Fatalf("%v, ждали %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v, ждали %v: эталон первым и без повтора", got, want)
		}
	}
	if got := withGold("books/1#1", []string{"books/2#2", "books/3#3"}, 2); len(got) != 2 {
		t.Errorf("предел 2 не соблюдён: %v", got)
	}
}

// AUC: полное разделение — 1, обратное — 0, совпадение — 0,5, пустая сторона — 0,5.
func TestAucOf(t *testing.T) {
	cases := []struct {
		pos, neg []float64
		want     float64
	}{
		{[]float64{3, 4}, []float64{1, 2}, 1},
		{[]float64{1, 2}, []float64{3, 4}, 0},
		{[]float64{1, 1}, []float64{1, 1}, 0.5},
		{[]float64{2}, []float64{1, 3}, 0.5},
		{nil, []float64{1}, 0.5},
	}
	for _, c := range cases {
		if got := aucOf(c.pos, c.neg); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("aucOf(%v, %v) = %.3f, ждали %.3f", c.pos, c.neg, got, c.want)
		}
	}
}
