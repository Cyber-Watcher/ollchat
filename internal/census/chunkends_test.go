package census

import "testing"

// TestChunkEnds — признаки концов куска (Е4): закрывающие кавычки и скобки не мешают,
// двоеточие закрывает, запятая и буква — нет; строчная буква в начале — середина фразы.
func TestChunkEnds(t *testing.T) {
	for _, c := range []struct {
		text string
		end  bool
	}{
		{"Абзац кончился.", true},
		{"Сказано «так».» ", true},
		{"Пример (см. рис. 2).", true},
		{"Перечислим:", true},
		{"и затем вызывается функция,", false},
		{"обрезано посреди слова", false},
		{"", false},
	} {
		if got := endsSentence(c.text); got != c.end {
			t.Errorf("endsSentence(%q) = %v, ждали %v", c.text, got, c.end)
		}
	}
	if !tailLine("Абзац.\n\nОб авторе книги\n") || tailLine("одна длинная строка без перевода") {
		t.Error("tailLine ошибается")
	}
	if !startsMidSentence("  продолжение фразы") || startsMidSentence("Новое предложение") ||
		startsMidSentence("— Реплика") || startsMidSentence("42 — число") {
		t.Error("startsMidSentence ошибается")
	}
}
