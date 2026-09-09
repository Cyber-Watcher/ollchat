package graph

import "testing"

// граф: 1—2 (вес 3), 2—3 (вес 1) — одна часть; 4—5 (вес 1) — вторая часть.
func testAdj() (map[uint32]map[uint32]float64, []uint32) {
	adj := map[uint32]map[uint32]float64{}
	add := func(a, b uint32, w float64) {
		if adj[a] == nil {
			adj[a] = map[uint32]float64{}
		}
		adj[a][b] = w
	}
	add(1, 2, 3)
	add(2, 1, 3)
	add(2, 3, 1)
	add(3, 2, 1)
	add(4, 5, 1)
	add(5, 4, 1)
	return adj, []uint32{1, 2, 3, 4, 5}
}

func TestStructureCountsPairsOnce(t *testing.T) {
	adj, order := testAdj()
	got := structure(adj, order, 7, 2)

	if got.Nodes != 5 {
		t.Errorf("понятий со связями %d, ожидалось 5", got.Nodes)
	}
	// Живых семь, со связями пять — значит двое одиночек.
	if got.Isolated != 2 {
		t.Errorf("одиночек %d, ожидалось 2", got.Isolated)
	}
	if got.Pairs != 3 {
		t.Errorf("пар %d, ожидалось 3", got.Pairs)
	}
	// Пара 1—2 подтверждена трижды, две другие — по разу.
	if got.PairsOnce != 2 {
		t.Errorf("пар на одном подтверждении %d, ожидалось 2", got.PairsOnce)
	}
	if got.Parts != 2 || got.Largest != 3 {
		t.Errorf("частей %d, наибольшая %d; ожидалось 2 и 3", got.Parts, got.Largest)
	}
}

func TestStructureShares(t *testing.T) {
	adj, order := testAdj()
	got := structure(adj, order, 7, 2)

	if s := got.OnceShare(); s != 66 {
		t.Errorf("доля связей на одном подтверждении %d%%, ожидалось 66%%", s)
	}
	if s := got.LargestShare(); s != 60 {
		t.Errorf("доля наибольшей части %d%%, ожидалось 60%%", s)
	}
	if s := got.IsolatedShare(); s != 28 {
		t.Errorf("доля одиночек %d%%, ожидалось 28%%", s)
	}
}

// Пустой граф не должен делить на ноль ни в одной доле.
func TestStructureEmpty(t *testing.T) {
	var got Structure
	if got.OnceShare() != 0 || got.LargestShare() != 0 || got.IsolatedShare() != 0 {
		t.Fatalf("на пустом графе доли не нулевые: %+v", got)
	}
	empty := structure(map[uint32]map[uint32]float64{}, nil, 0, 0)
	if empty.Nodes != 0 || empty.Parts != 0 || empty.Isolated != 0 {
		t.Fatalf("пустой граф дал %+v", empty)
	}
}

// Меньше живых понятий, чем узлов со связями, быть не может, но если реестр
// прочитан не до конца, отрицательного числа одиночек программа выдать не должна.
func TestStructureNeverNegativeIsolated(t *testing.T) {
	adj, order := testAdj()
	if got := structure(adj, order, 2, 0); got.Isolated != 0 {
		t.Fatalf("одиночек %d, ожидалось 0", got.Isolated)
	}
}

// Хабы считаются правилом поиска, а не по смежности: понятие 2 связано
// с двумя соседями в обе стороны, и правило видит у него четырёх.
func TestCountHubsUsesSearchRule(t *testing.T) {
	adj, order := testAdj()
	neigh := func(id uint32) int { return len(adj[id]) * 2 } // связь в обе стороны

	if n := countHubs(adj, order, 4, neigh); n != 1 {
		t.Fatalf("хабов %d при пороге 4, ожидался 1 (понятие 2)", n)
	}
	// Порог 6 не берёт никого: у самого связного понятия соседей четверо.
	if n := countHubs(adj, order, 6, neigh); n != 0 {
		t.Errorf("при пороге 6 насчитано %d хабов", n)
	}
}

// Кандидатов отсеивает грубый предел, но ни один настоящий хаб не должен
// потеряться: правило не может насчитать больше двойного числа соседей.
func TestCountHubsKeepsEveryCandidate(t *testing.T) {
	adj, order := testAdj()
	seen := map[uint32]bool{}
	neigh := func(id uint32) int { seen[id] = true; return len(adj[id]) * 2 }

	countHubs(adj, order, 4, neigh)
	if !seen[2] {
		t.Fatal("понятие 2 не проверено правилом, а оно хаб")
	}
}

// Нулевой порог означает «хабов не считаем»: правило может быть выключено.
func TestCountHubsWithoutLimit(t *testing.T) {
	adj, order := testAdj()
	if n := countHubs(adj, order, 0, func(uint32) int { return 100 }); n != 0 {
		t.Fatalf("при выключенном пороге насчитано хабов %d", n)
	}
	if n := countHubs(adj, order, 4, nil); n != 0 {
		t.Fatalf("без правила насчитано хабов %d", n)
	}
}
