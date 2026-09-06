package graph

import (
	"math"
	"testing"
)

// blendAdj — маленький граф связей: пары понятий с весом единица.
func blendAdj(pairs ...[2]uint32) map[uint32]map[uint32]float64 {
	adj := map[uint32]map[uint32]float64{}
	for _, p := range pairs {
		for _, ab := range [][2]uint32{{p[0], p[1]}, {p[1], p[0]}} {
			if adj[ab[0]] == nil {
				adj[ab[0]] = map[uint32]float64{}
			}
			adj[ab[0]][ab[1]] = 1
		}
	}
	return adj
}

// Связь по смыслу усиливается, связь мимо смысла — ослабляется.
//
// Ради этого подмешивание и делается: модулярность видит только вес и не
// отличает «LLM → эмбеддинг» от «LLM → Notion», а векторы отличают.
func TestSenseBlendStrengthensCloseWeakensFar(t *testing.T) {
	// Понятия 1..10 — одна группа смысла, 11..20 — другая.
	g := &Graph{vecs: twoGroupVectors(20)}
	adj := blendAdj(
		[2]uint32{1, 2},  // обе из первой группы: близко
		[2]uint32{1, 15}, // через границу: далеко
	)

	st := g.blendBySense(adj, 1)
	if !st.Ready {
		t.Fatalf("подмешивание не сработало: %+v", st)
	}
	if st.Blended != 2 || st.NoVector != 0 {
		t.Errorf("тронуто рёбер %d, без вектора %d — ожидалось 2 и 0", st.Blended, st.NoVector)
	}
	if adj[1][2] <= 1 {
		t.Errorf("связь по смыслу не усилена: вес %.3f", adj[1][2])
	}
	if adj[1][15] >= 1 {
		t.Errorf("связь мимо смысла не ослаблена: вес %.3f", adj[1][15])
	}
	// Обе стороны правятся одинаково: Louvain рассчитан на неориентированный граф.
	if adj[1][2] != adj[2][1] || adj[1][15] != adj[15][1] {
		t.Errorf("граф перестал быть неориентированным: %.3f/%.3f и %.3f/%.3f",
			adj[1][2], adj[2][1], adj[1][15], adj[15][1])
	}
}

// β = 0 не трогает ничего: это опорное состояние опыта.
func TestSenseBlendZeroBetaChangesNothing(t *testing.T) {
	g := &Graph{vecs: twoGroupVectors(20)}
	adj := blendAdj([2]uint32{1, 2}, [2]uint32{1, 15})

	st := g.blendBySense(adj, 0)
	if st.Ready || st.Blended != 0 {
		t.Errorf("при β = 0 что-то посчиталось: %+v", st)
	}
	for a, row := range adj {
		for b, w := range row {
			if w != 1 {
				t.Errorf("вес связи %d—%d изменился на %.3f", a, b, w)
			}
		}
	}
}

// Вес не уходит в ноль и не становится отрицательным даже при большой β.
//
// Отрицательный вес ломает модулярность: сумма весов в знаменателе перестаёт
// быть нормировкой, и приросты теряют смысл.
func TestSenseBlendNeverGoesNegative(t *testing.T) {
	g := &Graph{vecs: twoGroupVectors(20)}
	// Цепочка по всем двадцати понятиям: фон меряется по понятиям самого графа
	// связей, и на графе из одного ребра он равен близости этого ребра — тогда
	// отклонение от фона нулевое и множитель не работает вовсе. Это не изъян
	// меры, а свойство: фон обязан считаться по чему-то, кроме проверяемой пары.
	pairs := make([][2]uint32, 0, 19)
	for i := uint32(1); i < 20; i++ {
		pairs = append(pairs, [2]uint32{i, i + 1})
	}
	adj := blendAdj(pairs...)

	g.blendBySense(adj, 1000)
	// Ребро 10—11 идёт через границу групп: близость ноль при фоне около 0.5.
	if w := adj[10][11]; w <= 0 {
		t.Fatalf("вес связи ушёл в %.6f", w)
	}
	if w := adj[10][11]; w > senseMinFactor+1e-9 {
		t.Errorf("вес %.6f выше нижней границы %.3f — ограничение не сработало", w, senseMinFactor)
	}
}

// Понятие без вектора связь не меняет: иначе в разбиение подмешался бы
// не смысл, а возраст понятия — вектор есть у тех, кто попал в граф раньше.
func TestSenseBlendLeavesVectorlessEdgesAlone(t *testing.T) {
	// Векторы есть у 1..20, у понятия 21 их нет.
	g := &Graph{vecs: twoGroupVectors(20)}
	adj := blendAdj([2]uint32{1, 21}, [2]uint32{1, 2})

	st := g.blendBySense(adj, 2)
	if st.NoVector != 1 {
		t.Errorf("рёбер без вектора насчитано %d, ожидалось одно", st.NoVector)
	}
	if adj[1][21] != 1 || adj[21][1] != 1 {
		t.Errorf("связь с понятием без вектора изменена: %.3f", adj[1][21])
	}
}

// Фон меряется по графу, а не назначается: на векторах двух перпендикулярных
// групп он обязан выйти около половины.
func TestBackgroundCosineMeasured(t *testing.T) {
	v := twoGroupVectors(400)
	ids := make([]uint32, 0, 400)
	for i := uint32(1); i <= 400; i++ {
		ids = append(ids, i)
	}
	got := backgroundCosine(v, ids)
	if math.Abs(got-0.5) > 0.05 {
		t.Errorf("фон %.3f, ожидался около 0.5", got)
	}
}

// Нет векторов — подмешивать нечем, и веса остаются как были.
func TestSenseBlendWithoutVectors(t *testing.T) {
	adj := blendAdj([2]uint32{1, 2})
	st := (&Graph{}).blendBySense(adj, 1)
	if st.Ready {
		t.Errorf("подмешивание объявило себя сработавшим без векторов: %+v", st)
	}
	if adj[1][2] != 1 {
		t.Errorf("вес изменён без векторов: %.3f", adj[1][2])
	}
}

// Разбиение с подмешанным смыслом на диск не пишется: β — измерительная
// ручка, файл тем о ней не помнит, и такое разбиение лежало бы неотличимым
// от обычного. Замер (PartitionOnly) идёт как прежде.
func TestBlendedPartitionIsNeverSaved(t *testing.T) {
	g := growGraph(t, 4)
	if err := g.Edges().Add(Edge{Src: 1, Dst: 2, Type: 1, Weight: 1,
		Evidence: ChunkKey{Doc: 1, Ord: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.BuildCommunitiesWith(CommunityOpts{Beta: 1}); err == nil {
		t.Fatal("разбиение с β записалось на диск")
	}
	if _, err := g.PartitionOnly(CommunityOpts{Beta: 1}); err != nil {
		t.Fatalf("замер с β не идёт: %v", err)
	}
	if _, err := g.BuildCommunitiesWith(CommunityOpts{}); err != nil {
		t.Fatalf("обычное разбиение не пишется: %v", err)
	}
}
