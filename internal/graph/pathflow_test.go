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

// Один и тот же узел обязан считаться хабом обоими обходами.
//
// ПОЧЕМУ ЗАВЕДЁН (25.09.2026). В замере Б5 от 20.09 на 81 паре из журнала
// прежний обход не дал НИ ОДНОЙ цепочки через хаб (500+ связей), а обход
// по потоку дал две, обе через `scikit-learn` с 533 связями. В записи этапа
// при этом стоит «хабы в середине по-прежнему запрещены» — значит либо
// запрет не работает, либо два обхода меряют разное.
//
// Меряют разное. Обход по шагам спрашивает `len(Edges.Neighbors(id))`,
// а `Neighbors` ключуется парой {сосед, направление}: сосед, связанный
// в обе стороны, входит в список ДВАЖДЫ. Обход по потоку считает соседей
// сам, картой по идентификатору, — каждый сосед один раз. На узле, где
// половина связей двусторонние, первая мера почти вдвое больше второй,
// и порог 500 срабатывает у одного обхода и молчит у другого.
//
// Тест НЕ решает, какая мера правильная (это решение владельца: уникальные
// соседи — честнее по смыслу слова «хаб», но порог 500 замерен 08.09.2026
// именно на завышенной мере, и его пришлось бы перемерить). Он держит
// инвариант: ответ на вопрос «хаб ли это» не должен зависеть от того,
// каким ключом человек включил `graph.path_flow`.
func TestChainHubLimitSameInBothPathModes(t *testing.T) {
	g := newGraphWith(t, "A", "серединка", "B")
	const a, mid, b = 1, 2, 3
	add := func(src, dst uint32) {
		if err := g.Edges().Add(Edge{Src: src, Dst: dst, Type: RelRelated, Weight: 1,
			Evidence: ChunkKey{Doc: 1, Ord: src*10 + dst}}); err != nil {
			t.Fatal(err)
		}
	}
	// Каждая связь середины — в обе стороны: так связи и лежат в живом
	// графе, когда книги говорят «X использует Y» и «Y применяется в X».
	both := func(x, y uint32) { add(x, y); add(y, x) }
	both(a, mid)
	both(mid, b)
	for i := 0; i < 2; i++ {
		id, _, err := g.Entities().Add(fmt.Sprintf("прочее%d", i), TypeConcept)
		if err != nil {
			t.Fatal(err)
		}
		both(mid, id)
	}

	uniq := map[uint32]bool{}
	for _, e := range g.edge.around(mid) {
		other := e.Dst
		if e.Dst == mid {
			other = e.Src
		}
		uniq[other] = true
	}
	byNeighbors := len(g.Edges().Neighbors(mid))
	t.Logf("середина: уникальных соседей %d, Neighbors даёт %d", len(uniq), byNeighbors)

	// Порог между двумя мерами: по Neighbors середина — хаб, по уникальным
	// соседям — нет. Если меры совпадают, такого порога не существует
	// и проверять нечего.
	limit := len(uniq) + 1
	if byNeighbors < limit {
		t.Skipf("меры совпали (%d и %d) — расхождения нет", len(uniq), byNeighbors)
	}
	g.rules.ChainHubLimit = limit

	g.rules.PathFlow = false
	_, byHops := g.Path("A", "B", 3)
	g.rules.PathFlow = true
	_, byFlow := g.Path("A", "B", 3)

	if byHops != byFlow {
		t.Fatalf("при пороге %d обходы разошлись: по шагам путь найден = %v, "+
			"по потоку = %v. Середина считается хабом по одной мере "+
			"(Neighbors = %d, сосед в обе стороны учтён дважды) и не считается "+
			"по другой (уникальных соседей %d)",
			limit, byHops, byFlow, byNeighbors, len(uniq))
	}
}
