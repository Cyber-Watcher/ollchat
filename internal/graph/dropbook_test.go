package graph

import (
	"testing"
)

// Отброшенная книга исчезает из выдачи, но реестр и журналы целы; откат
// возвращает её. Всё через журнал dropped-books.jsonl, дозаписью.
func TestDropAndRestoreBook(t *testing.T) {
	coll := t.TempDir()
	g, err := Create(coll, "t", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if g.Dropped().Dropped(10) {
		t.Fatal("книга 10 не должна быть отброшена изначально")
	}

	if err := g.DropBook(10, "/path/bad.pdf", "скан"); err != nil {
		t.Fatal(err)
	}
	if !g.Dropped().Dropped(10) || g.Dropped().Count() != 1 {
		t.Fatalf("книга 10 не отброшена: отброшено %d", g.Dropped().Count())
	}

	// Переоткрытие графа: решение переживает закрытие — оно в журнале.
	_ = g.Close()
	g2, err := Open(coll, 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if !g2.Dropped().Dropped(10) {
		t.Fatal("отбрасывание не пережило переоткрытие графа")
	}

	// Откат — обратная запись; последняя по книге побеждает.
	if err := g2.RestoreBook(10, "/path/bad.pdf"); err != nil {
		t.Fatal(err)
	}
	if g2.Dropped().Dropped(10) || g2.Dropped().Count() != 0 {
		t.Fatalf("возврат книги не сработал: отброшено %d", g2.Dropped().Count())
	}
}

// Куски отброшенной книги не попадают в подтверждения выдачи.
func TestDroppedBookHiddenFromEvidence(t *testing.T) {
	coll := t.TempDir()
	g, _ := Create(coll, "t", 100, Rules{})
	defer g.Close()
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(g.Mentions().Add(1, ChunkKey{Doc: 10, Ord: 1})) // книга 10
	must(g.Mentions().Add(1, ChunkKey{Doc: 20, Ord: 1})) // книга 20

	seeds := []FoundEntity{{Entity: Entity{ID: 1}}}
	ev := g.evidence(seeds, 10)
	if len(ev) != 2 {
		t.Fatalf("до отбрасывания подтверждений %d, ожидалось 2", len(ev))
	}
	must(g.DropBook(10, "", ""))
	ev = g.evidence(seeds, 10)
	if len(ev) != 1 || ev[0].Doc != 20 {
		t.Fatalf("после отбрасывания книги 10 подтверждения: %v", ev)
	}
}

// Отброшенная книга невидима поиску целиком, а не только в цитатах: ни её
// связей среди соседей, ни её упоминаний в счёте, ни цепочки через её связь,
// ни понятия, известного только из неё, на смысловом входе (аудит
// 07.10.2026, 4.5).
func TestDroppedBookInvisibleToSearch(t *testing.T) {
	g := newGraphWith(t, "альфа", "бета", "гамма")
	defer g.Close()
	a, b, c := uint32(1), uint32(2), uint32(3)
	kept, dropped := ChunkKey{Doc: 10, Ord: 1}, ChunkKey{Doc: 20, Ord: 1}
	must(t, g.Mentions().Add(a, kept))
	must(t, g.Mentions().Add(a, dropped))
	must(t, g.Mentions().Add(c, dropped)) // «гамма» — только из отброшенной книги
	must(t, g.Edges().Add(Edge{Src: a, Dst: b, Type: RelUses, Weight: 1, Evidence: kept}))
	must(t, g.Edges().Add(Edge{Src: a, Dst: c, Type: RelUses, Weight: 1, Evidence: dropped}))
	// Векторы: у каждого понятия своя ось, вопрос — ровно на оси «гаммы».
	const dim = 4
	vecs := make([]int8, 3*dim)
	for i := 0; i < 3; i++ {
		vecs[i*dim+i] = 127
	}
	must(t, g.vecs.save("проба", "", dim, vecs))
	query := []int8{0, 0, 127, 0}
	if res := g.Search("zzz", SearchOpts{QueryVector: query}); len(res.Entities) != 1 || res.Entities[0].ID != c {
		t.Fatalf("до отбрасывания смысловой вход должен найти «гамму»: %+v", res.Entities)
	}
	if _, ok := g.Path("альфа", "гамма", 3); !ok {
		t.Fatal("до отбрасывания цепочка альфа—гамма должна быть")
	}

	must(t, g.DropBook(20, "", ""))
	card, ok := g.Entity("альфа", SearchOpts{})
	if !ok {
		t.Fatal("понятие «альфа» не нашлось")
	}
	if card.Mentions != 1 || card.Books != 1 {
		t.Errorf("упоминаний %d в %d книгах, ожидалось 1 и 1 — отброшенная книга в счёте", card.Mentions, card.Books)
	}
	if card.NeighborsTotal != 1 || len(card.Neighbors) != 1 || card.Neighbors[0].ID != b {
		t.Errorf("соседи «альфы»: %+v (всего %d), ожидалась только «бета»", card.Neighbors, card.NeighborsTotal)
	}
	if _, ok := g.Path("альфа", "гамма", 3); ok {
		t.Error("цепочка идёт через связь отброшенной книги")
	}
	if res := g.Search("zzz", SearchOpts{QueryVector: query}); len(res.Entities) != 0 {
		t.Errorf("смысловой вход предлагает понятие, известное только из отброшенной книги: %+v", res.Entities)
	}
}
