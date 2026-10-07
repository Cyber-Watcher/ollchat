package stats

import (
	"reflect"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// Персонализированный ранг (-ppr) повторяем от запуска к запуску, а равный
// вес решается номером понятия (аудит 07.10.2026). Звезда даёт лучам ровно
// поровну: прежде их порядок брался из обхода карты и менялся между вызовами.
func TestPersonalRankIsDeterministic(t *testing.T) {
	g, err := graph.OpenOrCreate(t.TempDir(), "проба", 10, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	add := func(name string) uint32 {
		id, _, err := g.Entities().Add(name, "понятие")
		if err != nil || id == 0 {
			t.Fatalf("понятие %q: id=%d, %v", name, id, err)
		}
		return id
	}
	hub := add("граф знаний")
	var rays []uint32
	for _, n := range []string{"вершина", "ребро", "сообщество", "путь", "тройка", "онтология"} {
		ray := add(n)
		rays = append(rays, ray)
		if err := g.Edges().Add(graph.Edge{Src: hub, Dst: ray, Weight: 1, Evidence: graph.ChunkKey{Doc: 1, Ord: ray}}); err != nil {
			t.Fatal(err)
		}
	}
	first := personalRank(g, hub, 3, 0.85, 200)
	for i := 0; i < 30; i++ {
		if got := personalRank(g, hub, 3, 0.85, 200); !reflect.DeepEqual(got, first) {
			t.Fatalf("прогон %d дал %v, первый — %v", i, got, first)
		}
	}
	if !reflect.DeepEqual(first, rays) {
		t.Errorf("лучи звезды с равным весом: %v, ожидался порядок номеров %v", first, rays)
	}
}
