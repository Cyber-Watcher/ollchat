package graph

import "testing"

// Соседние куски одной книги — один источник; куски из разных мест — разные.
func TestCorroborationCollapsesAdjacentChunks(t *testing.T) {
	g := newGraphWith(t, "горутина", "канал", "рантайм", "планировщик")
	defer g.Close()
	add := func(src, dst uint32, doc, ord uint32) {
		if err := g.Edges().Add(Edge{Src: src, Dst: dst, Type: RelUses, Weight: 1,
			Evidence: ChunkKey{Doc: doc, Ord: ord}}); err != nil {
			t.Fatal(err)
		}
	}
	add(1, 2, 7, 10) // одна фраза в зоне перекрытия — два соседних куска
	add(1, 2, 7, 11)
	add(1, 3, 7, 10) // два разных места одной книги
	add(1, 3, 7, 30)
	add(2, 4, 8, 5) // один кусок

	c := g.Corroboration()
	if c.Pairs != 3 || c.Single != 1 || c.AdjacentOnly != 1 {
		t.Fatalf("пар %d, на одном куске %d, только соседние %d", c.Pairs, c.Single, c.AdjacentOnly)
	}
	if c.ConfChunks != 5 || c.ConfOrigins != 4 {
		t.Fatalf("подтверждений кусками %d, источниками %d", c.ConfChunks, c.ConfOrigins)
	}
	if c.SingleOriginShare() != 66 || c.SingleChunkShare() != 33 || c.InflationShare() != 20 {
		t.Fatalf("доли: источник %d%%, кусок %d%%, копии %d%%", c.SingleOriginShare(), c.SingleChunkShare(), c.InflationShare())
	}
}

func TestOriginsOf(t *testing.T) {
	for _, tc := range []struct {
		ords []uint32
		want int
	}{
		{[]uint32{3}, 1}, {[]uint32{4, 3}, 1}, {[]uint32{3, 4, 5}, 1},
		{[]uint32{3, 5}, 2}, {[]uint32{9, 1, 2, 7}, 3},
	} {
		if got := originsOf(tc.ords); got != tc.want {
			t.Errorf("%v: %d, ожидалось %d", tc.ords, got, tc.want)
		}
	}
}

// На весах по источникам соседние куски одной книги дают одно подтверждение
// в матрице смежности разбиения; по кускам — два.
func TestUndirectedByOriginsCollapsesAdjacent(t *testing.T) {
	for _, byOrigins := range []bool{false, true} {
		g, err := Create(collection(t), "books", 100, Rules{WeightsByOrigins: byOrigins})
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"горутина", "канал"} {
			if _, _, err := g.Entities().Add(n, TypeConcept); err != nil {
				t.Fatal(err)
			}
		}
		for _, ord := range []uint32{10, 11, 30} {
			if err := g.Edges().Add(Edge{Src: 1, Dst: 2, Type: RelUses, Weight: 1,
				Evidence: ChunkKey{Doc: 7, Ord: ord}}); err != nil {
				t.Fatal(err)
			}
		}
		adj, _ := g.undirected()
		want := 3.0
		if byOrigins {
			want = 2
		}
		if adj[1][2] != want || adj[2][1] != want {
			t.Errorf("byOrigins=%v: вес пары %v / %v, ожидалось %v", byOrigins, adj[1][2], adj[2][1], want)
		}
		g.Close()
	}
}
