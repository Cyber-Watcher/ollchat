package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mergeFixture: два понятия-двойника, у каждого своя половина связей
// и упоминаний, плюс третье понятие в стороне.
func mergeFixture(t *testing.T) *Graph {
	t.Helper()
	g := newGraphWith(t)
	add := func(name string, aliases ...string) uint32 {
		id, _, err := g.Entities().Add(name, TypeConcept, aliases...)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	ru := add("сборщик мусора")     // 1
	en := add("garbage collection") // 2
	heap := add("куча")             // 3
	pause := add("stop-the-world")  // 4

	// У русского понятия своя половина связей, у английского своя.
	for _, e := range []Edge{
		{Src: ru, Dst: heap, Type: RelRelated, Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 1}},
		{Src: en, Dst: pause, Type: RelRelated, Weight: 1, Evidence: ChunkKey{Doc: 2, Ord: 1}},
	} {
		if err := g.Edges().Add(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []struct {
		id uint32
		k  ChunkKey
	}{
		{ru, ChunkKey{Doc: 1, Ord: 1}}, {ru, ChunkKey{Doc: 1, Ord: 2}},
		{en, ChunkKey{Doc: 2, Ord: 1}},
	} {
		if err := g.Mentions().Add(m.id, m.k); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

// reopen закрывает и открывает граф заново: склейка обязана переживать это,
// иначе её нельзя применить отдельной командой.
func reopen(t *testing.T, g *Graph) *Graph {
	t.Helper()
	dir := g.Dir()
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := open(dir, Meta{Version: FormatVersion}, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Ради этого всё и делалось: после склейки половины связей сходятся у одного
// понятия. До склейки поиск через любое из имён давал половину знания.
func TestMergeJoinsBothHalves(t *testing.T) {
	g := mergeFixture(t)

	if n := len(g.Edges().Neighbors(1)); n != 1 {
		t.Fatalf("до склейки у русского имени соседей %d, ожидался 1", n)
	}
	if _, err := g.Merges().Add([]MergeRec{{From: 2, To: 1, Cos: 0.94, Verdict: "ДА"}}); err != nil {
		t.Fatal(err)
	}
	g = reopen(t, g)
	defer g.Close()

	nb := g.Edges().Neighbors(1)
	if len(nb) != 2 {
		t.Fatalf("после склейки соседей %d, ожидалось 2 (куча и stop-the-world)", len(nb))
	}
	if n := len(g.Mentions().Of(1)); n != 3 {
		t.Fatalf("упоминаний после склейки %d, ожидалось 3", n)
	}
}

// Поиск по имени поглощённого ведёт к выжившему, а не в пустоту.
func TestMergeLookupLeadsToSurvivor(t *testing.T) {
	g := mergeFixture(t)
	if _, err := g.Merges().Add([]MergeRec{{From: 2, To: 1}}); err != nil {
		t.Fatal(err)
	}
	g = reopen(t, g)
	defer g.Close()

	ent, ok := g.Entities().Lookup("garbage collection")
	if !ok {
		t.Fatal("имя поглощённого понятия перестало находиться вовсе")
	}
	if ent.ID != 1 || ent.Name != "сборщик мусора" {
		t.Fatalf("поиск привёл к %d (%s), ожидался выживший 1", ent.ID, ent.Name)
	}
	var seen bool
	for _, a := range ent.Aliases {
		if Normalize(a) == "garbage collection" {
			seen = true
		}
	}
	if !seen {
		t.Error("имя поглощённого должно остаться синонимом выжившего, иначе написание потеряно")
	}
}

// Склейка снимается удалением файла: граф возвращается в прежний вид.
//
// Это главное её свойство. Склейка необратима по смыслу, и единственная защита
// от неверного решения — то, что она лежит отдельно от реестра.
func TestMergeIsRemovable(t *testing.T) {
	g := mergeFixture(t)
	if _, err := g.Merges().Add([]MergeRec{{From: 2, To: 1}}); err != nil {
		t.Fatal(err)
	}
	g = reopen(t, g)
	if n := len(g.Edges().Neighbors(1)); n != 2 {
		t.Fatalf("склейка не применилась: соседей %d", n)
	}
	dir := g.Dir()
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if err := removeFile(dir, mergesFile); err != nil {
		t.Fatal(err)
	}
	g2, err := open(dir, Meta{Version: FormatVersion}, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if n := len(g2.Edges().Neighbors(1)); n != 1 {
		t.Fatalf("после снятия склейки соседей %d, ожидался 1 — граф не вернулся в прежний вид", n)
	}
	if ent, ok := g2.Entities().Get(2); !ok || ent.Name != "garbage collection" {
		t.Error("поглощённое понятие должно вернуться самостоятельным")
	}
}

// Цепочка A→B→C ведёт к C, а не к промежуточному B, которого уже нет.
func TestMergeChainResolves(t *testing.T) {
	g := mergeFixture(t)
	if _, err := g.Merges().Add([]MergeRec{{From: 2, To: 3}, {From: 3, To: 1}}); err != nil {
		t.Fatal(err)
	}
	g = reopen(t, g)
	defer g.Close()

	if got := g.Merges().Resolve(2); got != 1 {
		t.Fatalf("цепочка 2→3→1 разрешилась в %d, ожидалась 1", got)
	}
	if !g.Merges().Gone(3) {
		t.Error("промежуточное понятие тоже поглощено")
	}
}

// Повторная склейка той же пары не удваивает журнал.
func TestMergeIgnoresRepeats(t *testing.T) {
	g := mergeFixture(t)
	defer g.Close()

	n, err := g.Merges().Add([]MergeRec{{From: 2, To: 1}, {From: 2, To: 3}, {From: 4, To: 4}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("записано %d решений, ожидалось 1 (повтор и петля отбрасываются)", n)
	}
	if again, _ := g.Merges().Add([]MergeRec{{From: 2, To: 1}}); again != 0 {
		t.Errorf("повтор записан ещё %d раз", again)
	}
}

// Поглощённое понятие исчезает из Live, но остаётся в All.
//
// Разделение нужно счёту векторов: место вектора определяется номером понятия,
// и пропуск записей оставил бы дырки.
func TestMergeLiveVersusAll(t *testing.T) {
	g := mergeFixture(t)
	if _, err := g.Merges().Add([]MergeRec{{From: 2, To: 1}}); err != nil {
		t.Fatal(err)
	}
	g = reopen(t, g)
	defer g.Close()

	all, live := g.Entities().All(), g.Entities().Live()
	if len(all) != 4 {
		t.Fatalf("в реестре записей %d, ожидалось 4", len(all))
	}
	if len(live) != 3 {
		t.Fatalf("живых понятий %d, ожидалось 3", len(live))
	}
	for _, e := range live {
		if e.ID == 2 {
			t.Error("поглощённое понятие попало в Live")
		}
	}
}

// Круг в журнале (A→B и B→A): одно понятие пары выживает, и считаться
// поглощённым оно не должно. До 03.10.2026 Count возвращал число записей,
// и доктор занижал живые понятия на число кругов.
func TestMergeCountSkipsCycleSurvivor(t *testing.T) {
	g := mergeFixture(t)
	// Круг пишется мимо Add — так он лежит в рабочем журнале с 18.09.2026.
	line := `{"from":2,"to":3,"verdict":"ДА"}` + "\n" + `{"from":3,"to":2,"verdict":"ДА"}` + "\n" +
		`{"from":4,"to":1,"verdict":"ДА"}` + "\n"
	if err := os.WriteFile(filepath.Join(g.Dir(), mergesFile), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	g = reopen(t, g)
	defer g.Close()

	live := len(g.Entities().Live())
	if live != 2 {
		t.Fatalf("живых понятий %d, ожидалось 2: из круга 2↔3 выживает одно, 4 поглощено", live)
	}
	if got := g.Merges().Count(); got != 2 {
		t.Errorf("поглощено %d, ожидалось 2 (записей в журнале 3, одна — выживший круга)", got)
	}
	if st := g.Stats(0); st.Live() != live {
		t.Errorf("сводка называет живых %d, а Live() отдаёт %d — доктор и поиск расходятся", st.Live(), live)
	}
}

// Встречная склейка уже склеенной пары круга не создаёт — ни прямая,
// ни через цепочку, ни внутри одной пачки.
func TestMergeRefusesReverseDirection(t *testing.T) {
	g := mergeFixture(t)
	defer g.Close()

	if n, err := g.Merges().Add([]MergeRec{{From: 2, To: 1}}); err != nil || n != 1 {
		t.Fatalf("первая склейка: %d, %v", n, err)
	}
	if n, _ := g.Merges().Add([]MergeRec{{From: 1, To: 2}}); n != 0 {
		t.Errorf("встречная склейка 1→2 записана (%d) — в журнале круг", n)
	}
	// Через цепочку: 3→2→1 уже есть, 1→3 замкнул бы круг из трёх.
	if n, _ := g.Merges().Add([]MergeRec{{From: 3, To: 2}}); n != 1 {
		t.Fatalf("склейка 3→2 не записана: %d", n)
	}
	if n, _ := g.Merges().Add([]MergeRec{{From: 1, To: 3}}); n != 0 {
		t.Errorf("склейка 1→3 замкнула круг 3→2→1→3 (%d)", n)
	}
	// Внутри одной пачки.
	g2 := mergeFixture(t)
	defer g2.Close()
	if n, _ := g2.Merges().Add([]MergeRec{{From: 2, To: 3}, {From: 3, To: 2}}); n != 1 {
		t.Errorf("из пачки с парой навстречу записано %d, ожидалась 1", n)
	}
	if got := g.Merges().Count(); got != 2 {
		t.Errorf("поглощено %d, ожидалось 2", got)
	}
}

// Выключенные склейки (merges_enabled = false) выключены целиком: граф такой,
// будто журнала нет. До 07.10.2026 выключался лишь Resolve — поглощённое
// понятие находилось само, его связи доставались ещё и выжившему, а из Live
// оно пропадало: одна связь читалась с двух концов (аудит, 4.5).
func TestMergesOffIsWhole(t *testing.T) {
	g := mergeFixture(t)
	if _, err := g.Merges().Add([]MergeRec{{From: 2, To: 1}}); err != nil {
		t.Fatal(err)
	}
	dir := g.Dir()
	must(t, g.Close())
	off, err := open(dir, Meta{Version: FormatVersion}, Rules{MergesOff: true})
	if err != nil {
		t.Fatal(err)
	}
	defer off.Close()

	if n := off.Merges().Count(); n != 0 {
		t.Errorf("при выключенных склейках поглощено %d", n)
	}
	if n := len(off.Entities().Live()); n != 4 {
		t.Errorf("живых понятий %d, ожидалось 4 — поглощённое пропало", n)
	}
	// Связь английского имени — только у него, не у русского.
	if got := off.Edges().Of(1); len(got) != 1 || got[0].Dst != 3 {
		t.Errorf("связи русского имени: %+v — ожидалась одна, с кучей", got)
	}
	if got := off.Mentions().Of(1); len(got) != 2 {
		t.Errorf("упоминаний русского имени %d, ожидалось 2 — без чужих", len(got))
	}
	if ent, _ := off.Entities().Get(1); len(ent.Aliases) != 0 {
		t.Errorf("русскому имени приписаны синонимы поглощённого: %v", ent.Aliases)
	}
	// Связь видна с обоих концов одинаково: у кого сосед Y, тот и сосед Y.
	// В гибриде связь английского имени у русского читалась исходящей,
	// а у её второго конца — входящей от английского.
	for _, ent := range off.Entities().Live() {
		for _, nb := range off.Edges().Neighbors(ent.ID) {
			back := false
			for _, x := range off.Edges().Neighbors(nb.ID) {
				back = back || x.ID == ent.ID
			}
			if !back {
				t.Errorf("связь %d—%d видна только с одного конца", ent.ID, nb.ID)
			}
		}
	}
}

// Выжившего круга (A→B и B→A после ручной правки журнала) можно склеить
// с третьим понятием. До 07.10.2026 его новая склейка отбрасывалась молча,
// как «уже склеенное» (аудит, 4.5).
func TestCircleSurvivorCanBeMerged(t *testing.T) {
	g := mergeFixture(t)
	dir := g.Dir()
	must(t, g.Close())
	circle := `{"from":1,"to":2,"at":1}` + "\n" + `{"from":2,"to":1,"at":2}` + "\n"
	must(t, os.WriteFile(filepath.Join(dir, mergesFile), []byte(circle), 0o644))
	g2, err := open(dir, Meta{Version: FormatVersion}, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	m := g2.Merges()
	survivor := m.Resolve(1)
	if survivor != 1 && survivor != 2 {
		t.Fatalf("круг разрешился в %d", survivor)
	}
	if n, err := m.Add([]MergeRec{{From: survivor, To: 3}}); err != nil || n != 1 {
		t.Fatalf("склейка выжившего круга с третьим: записано %d, %v", n, err)
	}
	for _, id := range []uint32{1, 2} {
		if got := m.Resolve(id); got != 3 {
			t.Errorf("понятие %d ведёт к %d, ожидалось 3", id, got)
		}
	}
}

// Слишком длинная строка в журнале склеек пропускается, как битая, а склейки
// после неё действуют. До 07.10.2026 чтение на ней останавливалось молча,
// и снятие склеек затем переписывало журнал по усечённому — навсегда.
func TestMergesSurviveLongLine(t *testing.T) {
	g := mergeFixture(t)
	dir := g.Dir()
	must(t, g.Close())
	long := `{"from":3,"to":4,"why":"` + strings.Repeat("x", 2<<20) + `"}` + "\n"
	rec := `{"from":2,"to":1,"at":1}` + "\n"
	must(t, os.WriteFile(filepath.Join(dir, mergesFile), []byte(long+rec), 0o644))
	g2, err := open(dir, Meta{Version: FormatVersion}, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if got := g2.Merges().Resolve(2); got != 1 {
		t.Fatalf("склейка после длинной строки потеряна: 2 ведёт к %d", got)
	}
}
