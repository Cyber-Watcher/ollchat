package graph

import "testing"

// Понятие, на которое только ссылаются, обязано иметь соседей.
//
// До 01.09.2026 обход шёл по одному индексу «от кого», и у «канала» — на который
// ссылается сотня связей вида «горутина —использует→ канал» — в выдаче не было
// ни одного соседа. Спросить про такое понятие значило получить пустоту.
func TestIncomingEdgesGiveNeighbours(t *testing.T) {
	g, err := Create(t.TempDir(), "проба", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	gor, _, err := g.Entities().Add("горутина", "понятие")
	if err != nil {
		t.Fatal(err)
	}
	ch, _, err := g.Entities().Add("канал", "понятие")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Edges().Add(Edge{Src: gor, Dst: ch, Type: RelUses,
		Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 1}}); err != nil {
		t.Fatal(err)
	}

	// У того, кто ссылается, сосед был и раньше.
	if out := g.Edges().Neighbors(gor); len(out) != 1 || out[0].ID != ch || out[0].In {
		t.Fatalf("у «горутины» ожидался исходящий сосед «канал», вышло %+v", out)
	}
	// А у того, на кого ссылаются, — ради этого правка и делалась.
	in := g.Edges().Neighbors(ch)
	if len(in) != 1 {
		t.Fatalf("у «канала» ожидался один сосед, вышло %d: %+v", len(in), in)
	}
	if in[0].ID != gor {
		t.Errorf("сосед «канала» должен быть «горутина», вышло %d", in[0].ID)
	}
	if !in[0].In {
		t.Error("связь у «канала» входящая — направление обязано это помнить, иначе её напечатают наоборот")
	}
}

// Направление не теряется в выдаче поиска: связь печатается в ту сторону,
// в какую её извлекли из книги.
func TestSearchKeepsRelationDirection(t *testing.T) {
	g, err := Create(t.TempDir(), "проба", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	gor, _, _ := g.Entities().Add("горутина", "понятие")
	ch, _, _ := g.Entities().Add("канал", "понятие")
	if err := g.Edges().Add(Edge{Src: gor, Dst: ch, Type: RelUses,
		Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 1}}); err != nil {
		t.Fatal(err)
	}

	res := g.Search("канал", SearchOpts{TopEntities: 2, TopNeighbors: 5})
	if len(res.Relations) == 0 {
		t.Fatal("по «каналу» не нашлось ни одной связи")
	}
	r := res.Relations[0]
	if r.Src != "горутина" || r.Dst != "канал" {
		t.Errorf("связь напечатана как %s —%s→ %s, а в книге она «горутина —использует→ канал»",
			r.Src, r.Type, r.Dst)
	}
	if r.Evidence.Doc != 1 || r.Evidence.Ord != 1 {
		t.Errorf("подтверждение потеряно: %+v", r.Evidence)
	}
}

// Связь между двумя понятиями вопроса печатается один раз, а не дважды —
// исходящей у одного и входящей у другого (замечено 18.09.2026).
func TestSearchPrintsRelationBetweenSeedsOnce(t *testing.T) {
	g, err := Create(t.TempDir(), "проба", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	gor, _, _ := g.Entities().Add("горутина", "понятие")
	ch, _, _ := g.Entities().Add("канал", "понятие")
	mu, _, _ := g.Entities().Add("мьютекс", "понятие")
	for _, e := range []Edge{
		{Src: gor, Dst: ch, Type: RelUses, Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 1}},
		{Src: gor, Dst: mu, Type: RelUses, Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 2}},
	} {
		if err := g.Edges().Add(e); err != nil {
			t.Fatal(err)
		}
	}
	res := g.Search("горутина и канал", SearchOpts{TopEntities: 3, TopNeighbors: 5})
	n := 0
	for _, r := range res.Relations {
		if r.Src == "горутина" && r.Dst == "канал" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("связь «горутина → канал» напечатана %d раз, ожидался один: %+v", n, res.Relations)
	}
	if len(res.Relations) != 2 {
		t.Fatalf("связей %d, ожидалось 2 (вторая — с мьютексом): %+v", len(res.Relations), res.Relations)
	}
}
