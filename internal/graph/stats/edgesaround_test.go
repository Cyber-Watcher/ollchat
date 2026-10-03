package stats

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// edgesAround обязан видеть связи в обе стороны: понятие, на которое только
// ссылаются, с одним Edges.Of выходило «без связей» (singletons, domainnoise).
func TestEdgesAroundSeesIncoming(t *testing.T) {
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
	a, b, c := add("горутина"), add("канал"), add("планировщик")
	for _, ed := range []graph.Edge{
		{Src: a, Dst: b, Evidence: graph.ChunkKey{Doc: 1, Ord: 1}}, // a → b
		{Src: a, Dst: b, Evidence: graph.ChunkKey{Doc: 1, Ord: 2}}, // подтверждена дважды
		{Src: c, Dst: b, Evidence: graph.ChunkKey{Doc: 2, Ord: 1}}, // c → b
		{Src: b, Dst: c, Evidence: graph.ChunkKey{Doc: 2, Ord: 2}}, // b → c
	} {
		if err := g.Edges().Add(ed); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(g.Edges().Of(b)); n != 1 {
		t.Fatalf("Of(канал) дал %d связей, ожидалась 1 исходящая — проверка теряет смысл", n)
	}
	around := edgesAround(g, b)
	if len(around) != 4 {
		t.Fatalf("вокруг «канала» %d связей, ожидалось 4 (1 исходящая и 3 входящих): %+v", len(around), around)
	}
	in := 0
	for _, ed := range around {
		if ed.Dst == b {
			in++
		}
	}
	if in != 3 {
		t.Errorf("входящих %d, ожидалось 3", in)
	}
	// Понятие без входящих: ничего лишнего не добавляется.
	if n := len(edgesAround(g, a)); n != 2 {
		t.Errorf("вокруг «горутины» %d связей, ожидалось 2", n)
	}
}
