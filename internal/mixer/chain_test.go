package mixer

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Карта понятий для модели содержит цепочку между понятиями вопроса
// (этап 101, D1) — и **без ссылок на книги**: цитат в карте нет вовсе,
// а строка со страницей провоцировала бы сослаться на непрочитанное.
func TestMixIncludesChainWithoutCitations(t *testing.T) {
	dir := t.TempDir()
	base, err := kb.OpenBase(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	coll, err := base.Create("books", "проверочная коллекция")
	if err != nil {
		t.Fatal(err)
	}

	g, err := graph.Create(coll.Dir(), "books", 100, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	add := func(name string) uint32 {
		id, _, err := g.Entities().Add(name, graph.TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, mid, b := add("горутина"), add("канал"), add("рантайм")
	for _, e := range []graph.Edge{
		{Src: a, Dst: mid, Type: graph.RelUses, Weight: 2, Evidence: graph.ChunkKey{Doc: 1, Ord: 1}},
		{Src: mid, Dst: b, Type: graph.RelPart, Weight: 2, Evidence: graph.ChunkKey{Doc: 1, Ord: 2}},
	} {
		if err := g.Edges().Add(e); err != nil {
			t.Fatal(err)
		}
	}
	// По три упоминания на понятие: карта понятий отсекает одноразовые
	// (MinMentions: 2) — их 59% в настоящем графе и это в основном шум разбора.
	for _, id := range []uint32{a, mid, b} {
		for ord := uint32(1); ord <= 3; ord++ {
			if err := g.Mentions().Add(id, graph.ChunkKey{Doc: 1, Ord: ord}); err != nil {
				t.Fatal(err)
			}
		}
	}

	out := Build("как связаны горутина и рантайм",
		Deps{Coll: coll, Graph: g, GraphOn: true},
		Settings{Entities: 6, Neighbors: 4, Chain: true, Collection: "books"})

	if out.Chain == 0 {
		t.Fatalf("цепочка не попала в подмешивание: %+v", out)
	}
	if !strings.Contains(out.Text, "Путь из") {
		t.Errorf("в тексте для модели нет цепочки:\n%s", out.Text)
	}
	if strings.Contains(out.Text, "подтверждение:") {
		t.Errorf("в карте понятий не должно быть ссылок на книги:\n%s", out.Text)
	}
}
