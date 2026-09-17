package graph

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// graphWithChain: A→B, B→C, D→C — цепочка и отдельная склейка рядом.
func graphWithChain(t *testing.T) (g *Graph, a, b, c, d uint32) {
	t.Helper()
	g = newGraphWith(t, "stack allocator", "Stack allocation", "memory allocation", "allocator")
	a, b, c, d = 1, 2, 3, 4
	n, err := g.Merges().Add([]MergeRec{
		{From: a, To: b, Cos: 0.91, Verdict: "ДА", Why: "разное написание"},
		{From: b, To: c, Cos: 0.83, Verdict: "ДА", Why: "цепочка"},
		{From: d, To: c, Cos: 0.95, Verdict: "ДА", Why: "синоним"},
	})
	if err != nil || n != 3 {
		t.Fatalf("склейки не записались: %d, %v", n, err)
	}
	return g, a, b, c, d
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// Снимается ровно названная склейка: соседние стоят, цепочка ниже по течению
// остаётся внутри вернувшегося понятия, прежний журнал и след решения сохранены.
func TestUndoMergesRemovesOnlyNamedPair(t *testing.T) {
	g, a, b, c, d := graphWithChain(t)
	defer g.Close()
	if err := g.Mentions().Add(b, ChunkKey{Doc: 7, Ord: 1}); err != nil {
		t.Fatal(err)
	}
	if got := len(g.Mentions().Of(c)); got != 1 {
		t.Fatalf("до снятия упоминание поглощённого должно быть у выжившего, получено %d", got)
	}

	res, err := g.UndoMerges([][2]uint32{{b, c}}, "вердикт выносился другой паре", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Undone) != 1 || res.Undone[0].From != b || res.Carried != 1 {
		t.Fatalf("снято не то: %+v", res)
	}
	m := g.Merges()
	if m.Resolve(b) != b {
		t.Fatalf("понятие %d не вернулось: ведёт к %d", b, m.Resolve(b))
	}
	if m.Resolve(a) != b {
		t.Fatalf("поглощённое раньше понятие %d должно остаться внутри %d, ведёт к %d", a, b, m.Resolve(a))
	}
	if m.Resolve(d) != c {
		t.Fatalf("соседняя склейка %d → %d задета", d, c)
	}
	if got := len(g.Mentions().Of(c)); got != 0 {
		t.Fatalf("упоминание вернувшегося понятия осталось у прежнего выжившего: %d", got)
	}
	if got := len(g.Mentions().Of(b)); got != 1 {
		t.Fatalf("упоминание не вернулось к своему понятию: %d", got)
	}
	if e, ok := g.Entities().Get(b); !ok || e.Name != "Stack allocation" {
		t.Fatalf("вернувшееся понятие не находится по номеру: %+v %v", e, ok)
	}

	// На диске: журнал без снятой строки, копия прежнего, след решения.
	if got := lines(t, filepath.Join(g.dir, mergesFile)); len(got) != 2 {
		t.Fatalf("в журнале склеек %d строк, ожидалось 2", len(got))
	}
	if got := lines(t, res.Backup); len(got) != 3 {
		t.Fatalf("в копии прежнего журнала %d строк, ожидалось 3", len(got))
	}
	undone := lines(t, filepath.Join(g.dir, undoneMergesFile))
	if len(undone) != 1 || !strings.Contains(undone[0], "вердикт выносился другой паре") ||
		!strings.Contains(undone[0], `"cos":0.83`) {
		t.Fatalf("след снятого решения неполон: %v", undone)
	}

	// Заново открытый журнал читается так же — наложение живёт в файле.
	again, err := openMerges(g.dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Resolve(b) != b || again.Resolve(a) != b || again.Resolve(d) != c {
		t.Fatal("после повторного чтения журнала склейки разошлись с памятью")
	}
	// Снятую пару можно склеить снова — например, когда арбитр увидит её саму.
	if n, err := g.Merges().Add([]MergeRec{{From: b, To: c}}); err != nil || n != 1 {
		t.Fatalf("снятая пара не склеивается заново: %d, %v", n, err)
	}
}

// Сухой прогон ничего не пишет; пары, которых нет или которые ведут не туда,
// не снимаются и названы в отчёте.
func TestUndoMergesDryAndStaleList(t *testing.T) {
	g, a, b, c, d := graphWithChain(t)
	defer g.Close()
	before := lines(t, filepath.Join(g.dir, mergesFile))

	res, err := g.UndoMerges([][2]uint32{{b, c}, {c, a}, {d, b}, {a, 0}}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Undone) != 2 { // b→c и a→«куда бы ни вело»
		t.Fatalf("к снятию %d записей, ожидалось 2: %+v", len(res.Undone), res.Undone)
	}
	if len(res.Missing) != 1 || res.Missing[0] != [2]uint32{c, a} {
		t.Fatalf("несуществующая склейка не названа: %+v", res.Missing)
	}
	if len(res.Mismatch) != 1 || res.Mismatch[0] != [3]uint32{d, b, c} {
		t.Fatalf("склейка, ведущая не туда, не названа: %+v", res.Mismatch)
	}
	after := lines(t, filepath.Join(g.dir, mergesFile))
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatal("сухой прогон изменил журнал склеек")
	}
	if _, err := os.Stat(filepath.Join(g.dir, undoneMergesFile)); err == nil {
		t.Fatal("сухой прогон оставил след снятия")
	}
	if g.Merges().Resolve(b) != c {
		t.Fatal("сухой прогон снял склейку в памяти")
	}
}

// Под идущей сборкой журнал не подменяется: сборка дописывает в него склейки
// при связывании, и подмена потеряла бы её запись.
func TestUndoMergesRefusesUnderBuild(t *testing.T) {
	g, _, b, c, _ := graphWithChain(t)
	defer g.Close()
	lock := filepath.Join(g.dir, lockFile)
	if err := os.WriteFile(lock, []byte(fmt.Sprintf("pid %d, начато 2026-09-17T23:00:00+03:00\n", os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(lock)
	_, err := g.UndoMerges([][2]uint32{{b, c}}, "", false)
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("под живым признаком сборки ожидался отказ, получено: %v", err)
	}
	if g.Merges().Resolve(b) != c {
		t.Fatal("склейка снята вопреки отказу")
	}
}
