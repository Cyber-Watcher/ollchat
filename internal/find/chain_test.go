package find

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// chainGraph — три понятия цепочкой: «горутина —использует→ канал —часть→ рантайм».
// Прямой связи между крайними нет, и это главное: цепочка нужна именно там.
func chainGraph(t *testing.T) *graph.Graph {
	t.Helper()
	g, err := graph.Create(t.TempDir(), "books", 100, graph.Rules{})
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
	edges := []graph.Edge{
		{Src: a, Dst: b, Type: graph.RelUses, Weight: 3, Evidence: graph.ChunkKey{Doc: 1, Ord: 1}},
		{Src: b, Dst: c, Type: graph.RelPart, Weight: 2, Evidence: graph.ChunkKey{Doc: 1, Ord: 2}},
	}
	for _, e := range edges {
		if err := g.Edges().Add(e); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func ents(names ...string) []graph.FoundEntity {
	out := make([]graph.FoundEntity, 0, len(names))
	for _, n := range names {
		out = append(out, graph.FoundEntity{Entity: graph.Entity{Name: n}})
	}
	return out
}

// Цепочка находится, когда прямой связи между понятиями вопроса нет.
func TestChainFoundBetweenDistantEntities(t *testing.T) {
	g := chainGraph(t)
	steps := Chain(g, ents("горутина", "рантайм"), nil, Opts{})
	if len(steps) != 2 {
		t.Fatalf("ожидалась цепочка из двух шагов, получено %v", steps)
	}
	if steps[0].From != "горутина" || steps[len(steps)-1].To != "рантайм" {
		t.Errorf("цепочка идёт не от того к тому: %v", steps)
	}
}

// Прямая связь уже отвечает на вопрос — цепочка поверх неё была бы шумом.
func TestChainSkippedWhenDirectRelationShown(t *testing.T) {
	g := chainGraph(t)
	rels := []graph.FoundRelation{{Src: "горутина", Dst: "канал"}}
	if steps := Chain(g, ents("горутина", "канал"), rels, Opts{}); steps != nil {
		t.Errorf("при прямой связи цепочка не нужна, получено %v", steps)
	}
	// Направление связи роли не играет: спросили «канал и горутина» — то же самое.
	rels = []graph.FoundRelation{{Src: "канал", Dst: "горутина"}}
	if steps := Chain(g, ents("горутина", "канал"), rels, Opts{}); steps != nil {
		t.Errorf("обратное направление тоже считается прямой связью, получено %v", steps)
	}
}

// Одно понятие, выключенный поиск и отсутствие пути — все три случая молчат.
func TestChainSilentWhenNothingToShow(t *testing.T) {
	g := chainGraph(t)
	if steps := Chain(g, ents("горутина"), nil, Opts{}); steps != nil {
		t.Errorf("одно понятие — цепочки быть не может: %v", steps)
	}
	if steps := Chain(g, ents("горутина", "рантайм"), nil, Opts{ChainHops: -1}); steps != nil {
		t.Errorf("ChainHops -1 выключает поиск цепочки: %v", steps)
	}
	if steps := Chain(nil, ents("горутина", "рантайм"), nil, Opts{}); steps != nil {
		t.Errorf("без графа цепочки нет: %v", steps)
	}
	if steps := Chain(g, ents("горутина", "неизвестное"), nil, Opts{}); steps != nil {
		t.Errorf("понятия нет в графе — цепочки нет: %v", steps)
	}
}

// Предел шагов соблюдается: цепочку длиннее заданного не показываем.
func TestChainRespectsHopLimit(t *testing.T) {
	g := chainGraph(t)
	if steps := Chain(g, ents("горутина", "рантайм"), nil, Opts{ChainHops: 1}); steps != nil {
		t.Errorf("при пределе в один шаг двухшаговая цепочка не показывается: %v", steps)
	}
}

// diamondGraph — от «горутина» до «рантайм» три пути в два шага: через
// «канал», «планировщик» и «стек»; прямой связи нет. count — graph.path_count.
func diamondGraph(t *testing.T, count int) *graph.Graph {
	t.Helper()
	g, err := graph.Create(t.TempDir(), "books", 100, graph.Rules{PathCount: count})
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
	a, z := add("горутина"), add("рантайм")
	for i, mid := range []string{"канал", "планировщик", "стек"} {
		m := add(mid)
		for _, e := range []graph.Edge{
			{Src: a, Dst: m, Type: graph.RelUses, Weight: 2, Evidence: graph.ChunkKey{Doc: 1, Ord: uint32(2*i + 1)}},
			{Src: m, Dst: z, Type: graph.RelPart, Weight: 2, Evidence: graph.ChunkKey{Doc: 1, Ord: uint32(2*i + 2)}},
		} {
			if err := g.Edges().Add(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	return g
}

// Несколько путей в выдаче поиска (этап 126, Я2) — тем же путём, что Search:
// Chains по правилу графа, раскладка по полям выдачи, печать Render. При
// path_count 0 и 1 выдача та же, что давала прежняя раскладка (одна Chain).
func TestSearchChainsFollowPathCount(t *testing.T) {
	q := ents("горутина", "рантайм")
	for _, tc := range []struct{ count, paths int }{{0, 1}, {1, 1}, {-1, 1}, {3, 3}} {
		g := diamondGraph(t, tc.count)
		var res Result
		res.setChains(Chains(g, q, nil, Opts{}))
		if got := 1 + len(res.ChainMore); len(res.Chain) == 0 || got != tc.paths {
			t.Fatalf("path_count=%d: путей %d, ожидалось %d", tc.count, got, tc.paths)
		}
		out := Render(res, false, nil)
		old := Render(Result{Chain: Chain(g, q, nil, Opts{})}, false, nil)
		if tc.paths == 1 {
			if out != old {
				t.Fatalf("path_count=%d: выдача изменилась:\n%s\nбыло:\n%s", tc.count, out, old)
			}
			continue
		}
		if n := strings.Count(out, "Другой путь из 2 шагов:"); n != 2 {
			t.Fatalf("path_count=3: других путей %d, ожидалось 2:\n%s", n, out)
		}
		for _, mid := range []string{"канал", "планировщик", "стек"} {
			if !strings.Contains(out, "горутина —использует→ "+mid) {
				t.Errorf("нет пути через %s:\n%s", mid, out)
			}
		}
	}
}
