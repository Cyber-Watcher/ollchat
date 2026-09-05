package graph

import (
	"path/filepath"
	"testing"
)

// Вклад книги — выборка по номеру книги, ничего в графе не меняет.
func TestBookContribution(t *testing.T) {
	dir := filepath.Join(t.TempDir(), DirName)
	g, err := Create(filepath.Dir(dir), "t", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	// Понятие 1 упомянуто в книгах 10 и 20; понятие 2 — только в книге 10.
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(g.Mentions().Add(1, ChunkKey{Doc: 10, Ord: 1}))
	must(g.Mentions().Add(1, ChunkKey{Doc: 20, Ord: 1}))
	must(g.Mentions().Add(2, ChunkKey{Doc: 10, Ord: 2}))
	must(g.Edges().Add(Edge{Src: 1, Dst: 2, Type: 1, Weight: 1, Evidence: ChunkKey{Doc: 10, Ord: 2}}))
	must(g.Edges().Add(Edge{Src: 1, Dst: 2, Type: 1, Weight: 1, Evidence: ChunkKey{Doc: 20, Ord: 1}}))

	c := g.Contribution(10)
	if c.Mentions != 2 {
		t.Errorf("упоминаний книги 10 = %d, ожидалось 2", c.Mentions)
	}
	if c.Edges != 1 {
		t.Errorf("связей книги 10 = %d, ожидалось 1", c.Edges)
	}
	if len(c.Entities) != 2 {
		t.Errorf("понятий книги 10 = %d, ожидалось 2", len(c.Entities))
	}
	// Понятие 2 держится только на книге 10 — выбросишь её, оно исчезнет.
	if len(c.OnlyHere) != 1 || c.OnlyHere[0] != 2 {
		t.Errorf("держится только на книге 10: %v, ожидалось [2]", c.OnlyHere)
	}
}

// У книги без вклада — пусто, а не паника.
func TestBookContributionEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), DirName)
	g, _ := Create(filepath.Dir(dir), "t", 100, Rules{})
	defer g.Close()
	c := g.Contribution(999)
	if c.Mentions != 0 || c.Edges != 0 || len(c.Entities) != 0 {
		t.Fatalf("вклад несуществующей книги не пуст: %+v", c)
	}
}
