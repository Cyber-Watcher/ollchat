package graph

import "testing"

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
