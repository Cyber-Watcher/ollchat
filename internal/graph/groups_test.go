package graph

import (
	"testing"
)

// Группа объединяет понятия, не сливая их: у каждого свои книги и связи,
// а Siblings отдаёт остальных членов группы. Всё через журнал, дозаписью.
func TestGroupsAddAndRead(t *testing.T) {
	coll := t.TempDir()
	g, err := Create(coll, "t", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	// goroutine(1) и goroutines(2) — про одно, но остаются раздельными.
	id, err := g.Groups().Add(GroupRec{Members: []uint32{1, 2}, Conf: 0.9, Why: "формы слова", From: "resolve"})
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("группе не выдан номер")
	}
	if gid, ok := g.Groups().GroupOf(1); !ok || gid != id {
		t.Fatalf("понятие 1 не в группе: %d %v", gid, ok)
	}
	sib := g.Groups().Siblings(1)
	if len(sib) != 1 || sib[0] != 2 {
		t.Fatalf("соседи понятия 1: %v, ожидалось [2]", sib)
	}
	if g.Groups().Count() != 1 {
		t.Fatalf("групп %d, ожидалась 1", g.Groups().Count())
	}

	// Переживает переоткрытие графа — решение в журнале.
	_ = g.Close()
	g2, err := Open(coll, 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if gid, ok := g2.Groups().GroupOf(2); !ok || gid != id {
		t.Fatal("группа не пережила переоткрытие")
	}
	// Проверка сохранённых метаданных — того, чего склейка не несла.
	m := g2.Groups().Members(id)
	if len(m) != 2 {
		t.Fatalf("состав группы: %v", m)
	}
}

// Undo снимает группу: последняя запись по номеру побеждает.
func TestGroupsUndo(t *testing.T) {
	coll := t.TempDir()
	g, _ := Create(coll, "t", 100, Rules{})
	defer g.Close()
	id, _ := g.Groups().Add(GroupRec{Members: []uint32{1, 2, 3}})
	if _, err := g.Groups().Add(GroupRec{ID: id, Undo: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Groups().GroupOf(1); ok {
		t.Fatal("понятие 1 осталось в снятой группе")
	}
	if g.Groups().Count() != 0 {
		t.Fatalf("групп после отмены %d, ожидалось 0", g.Groups().Count())
	}
}

// Нет групп — Siblings и GroupOf молчат, а не паникуют.
func TestGroupsEmpty(t *testing.T) {
	coll := t.TempDir()
	g, _ := Create(coll, "t", 100, Rules{})
	defer g.Close()
	if _, ok := g.Groups().GroupOf(5); ok {
		t.Fatal("в пустых группах что-то нашлось")
	}
	if len(g.Groups().Siblings(5)) != 0 {
		t.Fatal("соседи в пустых группах не пусты")
	}
}

// Пары превращаются в связные компоненты: (1,2) и (2,3) дают одну группу {1,2,3},
// а (5,6) — отдельную. Одиночки группами не становятся.
func TestGroupsFromPairs(t *testing.T) {
	coll := t.TempDir()
	g, _ := Create(coll, "t", 100, Rules{})
	defer g.Close()
	pairs := [][2]uint32{{1, 2}, {2, 3}, {5, 6}}
	groups, members, err := g.GroupsFromPairs(pairs, 0.8, "тест", "resolve")
	if err != nil {
		t.Fatal(err)
	}
	if groups != 2 || members != 5 {
		t.Fatalf("групп %d, понятий %d — ожидалось 2 и 5", groups, members)
	}
	// 1, 2, 3 — в одной группе.
	g1, _ := g.Groups().GroupOf(1)
	g3, _ := g.Groups().GroupOf(3)
	if g1 == 0 || g1 != g3 {
		t.Fatalf("1 и 3 должны быть в одной группе: %d vs %d", g1, g3)
	}
	// 5 — в другой.
	g5, _ := g.Groups().GroupOf(5)
	if g5 == g1 {
		t.Fatal("5 не должна быть в группе с 1")
	}
	if len(g.Groups().Members(g1)) != 3 {
		t.Fatalf("в группе 1-2-3 должно быть 3 члена")
	}
}

// union добавляет членов группы в выдачу; off — нет. Узлы не сливаются.
func TestSearchGroupUnion(t *testing.T) {
	coll := t.TempDir()
	g, err := Create(coll, "t", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	// Два понятия «про одно» + их упоминания, чтобы seed нашёлся по имени.
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	id1, _, err := g.Entities().Add("goroutine", "понятие")
	must(err)
	id2, _, err := g.Entities().Add("горутина", "понятие")
	must(err)
	must(g.Mentions().Add(id1, ChunkKey{Doc: 1, Ord: 1}))
	must(g.Mentions().Add(id2, ChunkKey{Doc: 2, Ord: 1}))
	if _, err := g.Groups().Add(GroupRec{Members: []uint32{id1, id2}, Conf: 0.9}); err != nil {
		t.Fatal(err)
	}

	// off: спросили goroutine — вернулось только оно.
	got := g.Search("goroutine", SearchOpts{TopEntities: 6, Groups: GroupOff})
	if len(got.Entities) != 1 {
		t.Fatalf("off: понятий %d, ожидалось 1", len(got.Entities))
	}

	// union: вернулись оба члена группы, но раздельными записями.
	got = g.Search("goroutine", SearchOpts{TopEntities: 6, Groups: GroupUnion})
	names := map[string]bool{}
	for _, e := range got.Entities {
		names[e.Name] = true
	}
	if !names["goroutine"] || !names["горутина"] {
		t.Fatalf("union: не оба члена группы: %v", names)
	}
}
