package graph

import (
	"fmt"
	"testing"
)

// Цепочка по потоку (этап 105, Б5): короткий путь через общее понятие
// проигрывает пути на шаг длиннее через узкие понятия, а прежний обход
// по числу шагов по-прежнему выбирает короткий.
func TestPathByFlowPrefersSpecificIntermediates(t *testing.T) {
	g := newGraphWith(t, "A", "общее", "B", "узкое1", "узкое2")
	const a, common, b, y, z = 1, 2, 3, 4, 5
	add := func(src, dst uint32) {
		if err := g.Edges().Add(Edge{Src: src, Dst: dst, Type: RelRelated, Weight: 1,
			Evidence: ChunkKey{Doc: 1, Ord: src*10 + dst}}); err != nil {
			t.Fatal(err)
		}
	}
	// Короткий путь: A → общее → B; у «общего» ещё тридцать связей.
	add(a, common)
	add(common, b)
	for i := 0; i < 30; i++ {
		id, _, err := g.Entities().Add(fmt.Sprintf("прочее%d", i), TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		add(common, id)
	}
	// Длинный путь: A → узкое1 → узкое2 → B.
	add(a, y)
	add(y, z)
	add(z, b)

	steps, ok := g.Path("A", "B", 4)
	if !ok || len(steps) != 2 || steps[0].To != "общее" {
		t.Fatalf("обход по шагам: ожидался A → общее → B, получено %+v", steps)
	}

	g.rules.PathFlow = true
	steps, ok = g.Path("A", "B", 4)
	if !ok || len(steps) != 3 || steps[0].To != "узкое1" || steps[1].To != "узкое2" {
		t.Fatalf("обход по потоку: ожидался A → узкое1 → узкое2 → B, получено %+v", steps)
	}
	// Предел шагов держит и поток: в два шага есть только путь через общее.
	if steps, ok = g.Path("A", "B", 2); !ok || len(steps) != 2 {
		t.Fatalf("при пределе в два шага ожидался путь через общее, получено %+v, %v", steps, ok)
	}
	// Хаб в середине запрещён и по потоку.
	g.rules.ChainHubLimit = 3
	if steps, ok = g.Path("A", "B", 2); ok {
		t.Fatalf("путь через хаб при запрете: %+v", steps)
	}
}
