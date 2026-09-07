package graph

import "testing"

// Подтверждения при равном числе понятий берутся по очереди книг, а не по
// номеру книги: книга, проиндексированная позже, иначе не попадала в пул
// вовсе (замер 06.09.2026, docs/eval/pairfind-0906.md).
func TestEvidenceInterleavesBooksOnTie(t *testing.T) {
	g, _ := graph(t)
	id, _, err := g.Entities().Add("CoreDNS", TypeTech)
	if err != nil {
		t.Fatal(err)
	}
	// Оригинал — книга 104 с десятью упоминаниями, перевод — 105 с десятью.
	for ord := uint32(1); ord <= 10; ord++ {
		for _, doc := range []uint32{104, 105} {
			if err := g.Mentions().Add(id, ChunkKey{Doc: doc, Ord: ord}); err != nil {
				t.Fatal(err)
			}
		}
	}
	seeds := []FoundEntity{{Entity: Entity{ID: id}}}
	got := g.evidence(seeds, 4)
	if len(got) != 4 {
		t.Fatalf("кандидатов %d, ждали 4", len(got))
	}
	byDoc := map[uint32]int{}
	for _, k := range got {
		byDoc[k.Doc]++
	}
	if byDoc[104] != 2 || byDoc[105] != 2 {
		t.Fatalf("пул разобрала одна книга: %v", byDoc)
	}
	// Порядок устойчив: первая по номеру книга первой в каждой паре.
	if got[0] != (ChunkKey{104, 1}) || got[1] != (ChunkKey{105, 1}) {
		t.Fatalf("очередь книг нарушена: %v", got[:2])
	}
}

// Кусок, где названы оба понятия, по-прежнему важнее любого куска с одним —
// очередь книг действует только внутри одного балла.
func TestEvidenceKeepsCoMentionsFirst(t *testing.T) {
	g, _ := graph(t)
	a, _, _ := g.Entities().Add("CoreDNS", TypeTech)
	b, _, _ := g.Entities().Add("Corefile", TypeTech)
	for ord := uint32(1); ord <= 5; ord++ {
		if err := g.Mentions().Add(a, ChunkKey{Doc: 1, Ord: ord}); err != nil {
			t.Fatal(err)
		}
	}
	// Оба понятия — только в книге с большим номером.
	both := ChunkKey{Doc: 9, Ord: 3}
	if err := g.Mentions().Add(a, both); err != nil {
		t.Fatal(err)
	}
	if err := g.Mentions().Add(b, both); err != nil {
		t.Fatal(err)
	}
	seeds := []FoundEntity{{Entity: Entity{ID: a}}, {Entity: Entity{ID: b}}}
	got := g.evidence(seeds, 3)
	if len(got) == 0 || got[0] != both {
		t.Fatalf("кусок с обоими понятиями не первый: %v", got)
	}
}
