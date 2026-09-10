package graph

import "testing"

// weightedAdj — три пары с разной опорой: 1—2 подтверждена трижды,
// 2—3 дважды, 4—5 один раз (и на ней держатся оба её понятия).
func weightedAdj() (map[uint32]map[uint32]float64, map[uint32]map[uint32]int) {
	adj := map[uint32]map[uint32]float64{}
	conf := map[uint32]map[uint32]int{}
	link := func(a, b uint32, w float64, n int) {
		for _, x := range []uint32{a, b} {
			if adj[x] == nil {
				adj[x] = map[uint32]float64{}
				conf[x] = map[uint32]int{}
			}
		}
		adj[a][b], adj[b][a] = w, w
		conf[a][b], conf[b][a] = n, n
	}
	link(1, 2, 3, 3)
	link(2, 3, 2, 2)
	link(4, 5, 1, 1)
	return adj, conf
}

func TestCutWeakDropsPairAndItsNodes(t *testing.T) {
	adj, conf := weightedAdj()
	pairs, nodes := cutWeak(adj, conf, 2)

	if pairs != 1 {
		t.Errorf("отсечено пар %d, ожидалась 1 (пара 4—5)", pairs)
	}
	// На единственной отсечённой связи держались оба её понятия.
	if nodes != 2 {
		t.Errorf("осталось без связей %d понятий, ожидалось 2", nodes)
	}
	if _, ok := adj[4]; ok {
		t.Error("понятие 4 осталось в матрице без единой связи")
	}
	if len(adj[1]) != 1 || len(adj[2]) != 2 {
		t.Errorf("пережившие связи повреждены: %v", adj)
	}
}

// Отсечение обязано быть симметричным: односторонний остаток дал бы Louvain
// разные степени у двух концов одной связи.
func TestCutWeakStaysSymmetric(t *testing.T) {
	adj, conf := weightedAdj()
	cutWeak(adj, conf, 3)

	for a, nbs := range adj {
		for b := range nbs {
			if _, ok := adj[b][a]; !ok {
				t.Fatalf("связь %d—%d осталась односторонней", a, b)
			}
		}
	}
}

func TestCutWeakWithoutThresholdChangesNothing(t *testing.T) {
	adj, conf := weightedAdj()
	pairs, nodes := cutWeak(adj, conf, 0)
	if pairs != 0 || nodes != 0 || len(adj) != 5 {
		t.Fatalf("порог 0 тронул граф: пар %d, понятий %d, узлов %d", pairs, nodes, len(adj))
	}
}

func TestWeakenOnceHalvesSinglePairsOnly(t *testing.T) {
	adj, conf := weightedAdj()
	n := weakenOnce(adj, conf, 0.5)

	if n != 1 {
		t.Errorf("ослаблено пар %d, ожидалась 1 (пара 4—5)", n)
	}
	if adj[4][5] != 0.5 || adj[5][4] != 0.5 {
		t.Errorf("одиночная пара не ослаблена симметрично: %v / %v", adj[4][5], adj[5][4])
	}
	// Подтверждённые не раз остаются как были.
	if adj[1][2] != 3 || adj[2][3] != 2 {
		t.Errorf("тронуты пары с опорой: %v", adj)
	}
}

// Множитель вне (0;1) бессмыслен: 0 означал бы отсечение (для него cutWeak),
// 1 и больше — усиление шума. Такой вызов не должен менять ничего.
func TestWeakenOnceIgnoresSillyFactors(t *testing.T) {
	for _, k := range []float64{0, 1, 2, -1} {
		adj, conf := weightedAdj()
		if n := weakenOnce(adj, conf, k); n != 0 || adj[4][5] != 1 {
			t.Errorf("множитель %v тронул граф: n=%d, вес %v", k, n, adj[4][5])
		}
	}
}

// Одиночность пары — это число подтверждений, а не вес после множителей видов:
// одиночное «связано» весом 0.5 всё равно одиночное, а два «связано» с суммой
// 1.0 — нет (ревизия 10.09.2026).
func TestOnceIsAboutConfirmationsNotWeight(t *testing.T) {
	adj, conf := weightedAdj()
	adj[4][5], adj[5][4] = 0.5, 0.5 // одиночное «связано» после множителя 0.5
	adj[2][3], adj[3][2] = 1, 1     // два подтверждения, ослабленные до 1.0
	if n := weakenOnce(adj, conf, 0.5); n != 1 || adj[4][5] != 0.25 || adj[2][3] != 1 {
		t.Errorf("ослаблено %d, веса 4—5 %v, 2—3 %v", n, adj[4][5], adj[2][3])
	}
	if pairs, _ := cutWeak(adj, conf, 2); pairs != 1 {
		t.Errorf("порог 2 должен отсечь только одиночную пару, отсёк %d", pairs)
	}
}
