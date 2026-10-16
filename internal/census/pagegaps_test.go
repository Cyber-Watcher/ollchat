package census

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// cover — книга на n страниц, покрыты перечисленные.
func cover(title string, n int, pages ...int) *pageCover {
	p := &pageCover{book: kb.BookRec{Title: title, Units: n}, covered: make([]bool, n+1)}
	for _, pg := range pages {
		p.covered[pg] = true
	}
	return p
}

// Этап 112, Л5: пустые страницы и самая длинная пустота подряд.
func TestPageGaps(t *testing.T) {
	// 20 страниц: покрыты 1–5 и 18–20, пустота 6–17 — 12 страниц подряд.
	scan := cover("вставленный скан", 20, 1, 2, 3, 4, 5, 18, 19, 20)
	if e, run, at := scan.gaps(); e != 12 || run != 12 || at != 6 {
		t.Fatalf("пусто %d, подряд %d с %d; ждали 12, 12 с 6", e, run, at)
	}
	whole := cover("целая", 4, 1, 2, 3, 4)
	if e, run, _ := whole.gaps(); e != 0 || run != 0 {
		t.Fatalf("целая книга: пусто %d, подряд %d", e, run)
	}
	// 10 страниц, пусты 2 и 9 — две пустоты по одной.
	spots := cover("пятна", 10, 1, 3, 4, 5, 6, 7, 8, 10)
	if e, run, at := spots.gaps(); e != 2 || run != 1 || at != 2 {
		t.Fatalf("пятна: пусто %d, подряд %d с %d; ждали 2, 1 с 2", e, run, at)
	}
	out := pageGapsReport(map[uint32]*pageCover{1: scan, 2: whole, 3: spots}, 5)
	// Сверка без выравнивания: пробелы колонок схлопываются в один.
	flat := strings.Join(strings.Fields(out), " ")
	for _, want := range []string{
		"книг PDF с числом страниц: 3; страниц 34, из них без единого куска 14 (0.412)",
		"книг с пустотой подряд от 10 страниц: 1",
		"ровно 0 1 до 5 % 0 5–20 % 1 20–50 % 0 больше 50 % 1",
		"12 из 20 подряд 12 с стр. 6 вставленный скан",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "целая") {
		t.Errorf("книга без пустот попала в список пустот:\n%s", out)
	}
}
