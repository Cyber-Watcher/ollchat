package graph

import (
	"strings"
	"testing"
)

// Этап 112, О5: выдержка карточки — окно вокруг имени понятия, а не начало куска;
// имя ищется отдельным словом, синоним — если имени нет.
func TestEntitySnippet(t *testing.T) {
	head := strings.Repeat("Чужой абзац про другое. ", 40) // 960 знаков до понятия
	text := head + "PostgreSQL uses Multi-Version Concurrency Control (MVCC) rather than locking."

	got := EntitySnippet(text, []string{"MVCC"}, 120)
	if !strings.Contains(got, "MVCC") || !strings.HasPrefix(got, "…") {
		t.Fatalf("окно не накрыло имя: %q", got)
	}
	if n := len([]rune(strings.TrimPrefix(strings.TrimSuffix(got, "…"), "…"))); n > 120 {
		t.Fatalf("окно длиннее предела: %d знаков", n)
	}
	// Имени нет, синоним есть — окно вокруг синонима.
	if got := EntitySnippet(text, []string{"многоверсионность", "Multi-Version Concurrency Control"}, 120); !strings.Contains(got, "Multi-Version") {
		t.Fatalf("синоним не найден: %q", got)
	}
	// Ни имени, ни синонима — начало куска, как прежде.
	if got := EntitySnippet(text, []string{"Raft"}, 60); !strings.HasPrefix(got, "Чужой абзац") {
		t.Fatalf("без имени ждали начало куска: %q", got)
	}
	// «Go» внутри «good» и «algorithm» — не имя.
	if at := NameAt("a good algorithm", []string{"Go"}); at != -1 {
		t.Fatalf("Go найдено внутри слова на %d", at)
	}
	if at := NameAt("written in Go, fast", []string{"Go"}); at != 11 {
		t.Fatalf("Go отдельным словом: место %d, ждали 11", at)
	}
}
