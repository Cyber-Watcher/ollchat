package tools

import (
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// addDiamondGraph кладёт рядом с коллекцией test граф-ромб: от «alpha» до
// «omega» три пути в два шага — через mid1, mid2 и mid3; прямой связи нет.
// Подтверждения — настоящие куски коллекции, чтобы печатались строки книг.
func addDiamondGraph(t *testing.T, base *kb.Base) {
	t.Helper()
	coll, err := base.Open("test")
	if err != nil {
		t.Fatal(err)
	}
	var keys []graph.ChunkKey
	if err := coll.EachChunkRef(kb.ChunkFilter{}, func(c kb.ChunkRef) error {
		keys = append(keys, graph.ChunkKey{Doc: c.Doc, Ord: c.Ord})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(keys) == 0 {
		t.Fatal("в коллекции нет кусков")
	}
	g, err := graph.Create(coll.Dir(), coll.Name(), coll.ChunkCount(), graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	add := func(name string) uint32 {
		id, _, err := g.Entities().Add(name, graph.TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, z := add("alpha"), add("omega")
	for i, mid := range []string{"mid1", "mid2", "mid3"} {
		m, k := add(mid), keys[i%len(keys)]
		for _, e := range []graph.Edge{
			{Src: a, Dst: m, Type: graph.RelUses, Weight: 2, Evidence: k},
			{Src: m, Dst: z, Type: graph.RelPart, Weight: 2, Evidence: k},
		} {
			if err := g.Edges().Add(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := g.Entities().SaveCounters(); err != nil {
		t.Fatal(err)
	}
	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
}

// pathRegistry — реестр с одним graph_path и правилом graph.path_count.
func pathRegistry(t *testing.T, base *kb.Base, baseDir string, count int) *Registry {
	t.Helper()
	reg, err := NewRegistry([]string{NameGraphPath}, Options{
		KB: base, KBDir: baseDir, KBDefault: "test", MaxOutputKB: 64,
		GraphRules: graph.Rules{PathCount: count},
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// graph_path и ключ graph.path_count (этап 126, Я2). При 0, 1 и отрицательном
// выдача побайтно та, что инструмент печатал до правки (Path, RenderPath
// и заметка о графе); при 3 под первой цепочкой — ещё два пути.
func TestGraphPathCount(t *testing.T) {
	_, base, baseDir := newKBRegistry(t)
	addDiamondGraph(t, base)

	coll, err := base.Open("test")
	if err != nil {
		t.Fatal(err)
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	steps, ok := g.Path("alpha", "omega", 4)
	if !ok || len(steps) != 2 {
		g.Close()
		t.Fatalf("в ромбе нет цепочки в два шага: %v %v", steps, ok)
	}
	first := graph.RenderPath(coll, "alpha", "omega", steps, ok, graph.RenderOpts{Collection: "test"})
	old := first + graphNote(g, coll)
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "подтверждение: ") {
		t.Fatalf("строки подтверждения нет — тест не проверяет книги:\n%s", first)
	}

	args := map[string]any{"from": "alpha", "to": "omega"}
	for _, n := range []int{0, 1, -1} {
		out, _, err := runTool(t, pathRegistry(t, base, baseDir, n), NameGraphPath, args)
		if err != nil {
			t.Fatal(err)
		}
		if out != old {
			t.Fatalf("path_count=%d: выдача изменилась:\n%s\nбыло:\n%s", n, out, old)
		}
	}

	out, _, err := runTool(t, pathRegistry(t, base, baseDir, 3), NameGraphPath, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, first) {
		t.Fatalf("первая цепочка не та, что была:\n%s\nбыло:\n%s", out, first)
	}
	if n := strings.Count(out, "Другой путь из 2 шагов:"); n != 2 {
		t.Fatalf("других путей %d, ожидалось 2:\n%s", n, out)
	}
	for _, mid := range []string{"mid1", "mid2", "mid3"} {
		if !strings.Contains(out, "alpha —использует→ "+mid) {
			t.Errorf("нет пути через %s:\n%s", mid, out)
		}
	}
	t.Logf("выдача при path_count = 3:\n%s", out)
}
