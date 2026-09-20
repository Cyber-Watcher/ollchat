package mixer

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// pairGraph — крошечный граф: «горутина —использует→ канал», у обоих
// понятий по три упоминания (карта отсекает одноразовые), и отдельное
// понятие «рантайм» без связей с ними.
func pairGraph(t *testing.T) (kb.Source, *graph.Graph) {
	t.Helper()
	dir := t.TempDir()
	base, err := kb.OpenBase(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll, err := base.Create("books", "проверочная коллекция")
	if err != nil {
		t.Fatal(err)
	}
	g, err := graph.Create(coll.Dir(), "books", 100, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })

	add := func(name string) uint32 {
		id, _, err := g.Entities().Add(name, graph.TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b, c := add("горутина"), add("канал"), add("рантайм")
	if err := g.Edges().Add(graph.Edge{Src: a, Dst: b, Type: graph.RelUses, Weight: 2,
		Evidence: graph.ChunkKey{Doc: 1, Ord: 1}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{a, b, c} {
		for ord := uint32(1); ord <= 3; ord++ {
			if err := g.Mentions().Add(id, graph.ChunkKey{Doc: 1, Ord: ord}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return coll, g
}

// Карта без чисел (mix.map_style = "plain"): ни «упоминаний», ни
// «подтверждений», зато запрет пересказывать карту — этап 105, Б1.б.
func TestMapPlainHasNoCounts(t *testing.T) {
	coll, g := pairGraph(t)
	deps := Deps{Coll: coll, Graph: g, GraphOn: true}
	base := Settings{Entities: 6, Neighbors: 4, Collection: "books"}

	counts := Build("как горутина использует канал", deps, base)
	if !strings.Contains(counts.Text, "подтверждений") || !strings.Contains(counts.Text, "упоминаний") {
		t.Fatalf("прежняя подача потеряла числа:\n%s", counts.Text)
	}

	plain := base
	plain.MapPlain = true
	out := Build("как горутина использует канал", deps, plain)
	if out.Empty() || out.Entities == 0 {
		t.Fatalf("карта без чисел не собралась: %+v", out)
	}
	for _, bad := range []string{"подтверждений", "упоминаний"} {
		if strings.Contains(out.Text, bad) {
			t.Errorf("в карте без чисел осталось %q:\n%s", bad, out.Text)
		}
	}
	if !strings.Contains(out.Text, "горутина —использует→ канал") {
		t.Errorf("связь пропала из карты:\n%s", out.Text)
	}
	if !strings.Contains(out.Text, "не упоминай") {
		t.Errorf("в шапке нет запрета пересказывать карту:\n%s", out.Text)
	}
}

// Карта только на вопрос о связи (mix.map_when = "pair"): вопрос об одном
// понятии карты не получает, вопрос о двух связанных — получает.
func TestMapPairOnly(t *testing.T) {
	coll, g := pairGraph(t)
	deps := Deps{Coll: coll, Graph: g, GraphOn: true}
	set := Settings{Entities: 6, Neighbors: 4, Collection: "books", MapPairOnly: true}

	if out := Build("что такое горутина", deps, set); !out.Empty() {
		t.Errorf("вопрос об одном понятии получил карту:\n%s", out.Text)
	}
	// Два понятия найдены, но связи между ними нет — это тоже не вопрос о связи.
	if out := Build("горутина и рантайм", deps, set); !out.Empty() {
		t.Errorf("два понятия без связи получили карту:\n%s", out.Text)
	}
	out := Build("как горутина использует канал", deps, set)
	if out.Empty() || out.Relations == 0 {
		t.Fatalf("вопрос о связи двух понятий остался без карты: %+v", out)
	}
	// Прежнее умолчание: карта на всякий вопрос, связавшийся с графом.
	set.MapPairOnly = false
	if out := Build("что такое горутина", deps, set); out.Empty() {
		t.Errorf("при map_when = always вопрос об одном понятии остался без карты")
	}
}
