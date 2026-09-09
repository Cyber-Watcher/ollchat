package graph

import "testing"

// brokenTheme — одна тема из двух половин без единой связи между ними:
// 1—2—3 (крупная) и 8—9 (мелкая). Ровно то, что Louvain иногда выдаёт
// и чего Leiden не допускает.
func brokenTheme() (map[uint32]map[uint32]float64, []Community) {
	adj := map[uint32]map[uint32]float64{}
	link := func(a, b uint32, w float64) {
		if adj[a] == nil {
			adj[a] = map[uint32]float64{}
		}
		if adj[b] == nil {
			adj[b] = map[uint32]float64{}
		}
		adj[a][b] += w
		adj[b][a] += w
	}
	link(1, 2, 5)
	link(2, 3, 5)
	link(8, 9, 5)

	com := Community{
		ID: 1, Level: 0, Parent: 7,
		Members: []uint32{2, 1, 3, 8, 9}, // порядок — по убыванию упоминаний
		Title:   "тема", Summary: "описание", Rating: 9, Why: "почему",
		Key: []string{"ключ"}, Books: []string{"книга"},
	}
	return adj, []Community{com}
}

func TestSplitDisconnectedCutsThemeInTwo(t *testing.T) {
	adj, list := brokenTheme()
	out, split := splitDisconnected(adj, list)

	if split != 1 {
		t.Fatalf("разрезано тем %d, ожидалась 1", split)
	}
	if len(out) != 2 {
		t.Fatalf("получилось тем %d, ожидалось 2: %+v", len(out), out)
	}

	big, small := out[0], out[1]
	if len(big.Members) != 3 || len(small.Members) != 2 {
		t.Errorf("части неверного размера: %v и %v", big.Members, small.Members)
	}
	// Крупнейшая часть сохраняет номер темы и её описание.
	if big.ID != 1 || big.Summary != "описание" || big.Rating != 9 {
		t.Errorf("крупная часть потеряла имя темы: %+v", big)
	}
	// Мелкая получает новый номер и идёт на описание заново.
	if small.ID != 8 {
		t.Errorf("номер новой темы %d, ожидался 8 (наименьшее понятие части)", small.ID)
	}
	if small.Summary != "" || small.Title != "" || small.Rating != 0 {
		t.Errorf("описание досталось обеим частям: %+v", small)
	}
	// Родитель у частей общий: наверху они по-прежнему одна тема.
	if small.Parent != 7 || big.Parent != 7 {
		t.Errorf("части потеряли родителя: %d и %d", big.Parent, small.Parent)
	}
}

// Порядок участников внутри части — из исходной темы: первыми самые
// упоминаемые, по ним модель называет тему.
func TestSplitKeepsMemberOrder(t *testing.T) {
	adj, list := brokenTheme()
	out, _ := splitDisconnected(adj, list)

	want := []uint32{2, 1, 3}
	for i, id := range want {
		if out[0].Members[i] != id {
			t.Fatalf("порядок участников сбит: %v, ожидалось %v", out[0].Members, want)
		}
	}
}

// Связная тема разрезу не подлежит и обязана пройти насквозь нетронутой.
func TestSplitLeavesConnectedThemeAlone(t *testing.T) {
	adj, _ := brokenTheme()
	whole := Community{ID: 1, Level: 0, Members: []uint32{1, 2, 3}, Summary: "цело"}

	out, split := splitDisconnected(adj, []Community{whole})
	if split != 0 || len(out) != 1 || out[0].Summary != "цело" {
		t.Fatalf("связная тема тронута: split=%d, %+v", split, out)
	}
}

// Уровень 1 — объединение мелких тем, его несвязность не ошибка.
func TestSplitIgnoresUpperLevel(t *testing.T) {
	adj, list := brokenTheme()
	list[0].Level = 1

	out, split := splitDisconnected(adj, list)
	if split != 0 || len(out) != 1 {
		t.Fatalf("тема верхнего уровня разрезана: split=%d, тем %d", split, len(out))
	}
}

// После разреза несвязных тем не остаётся — это и есть проверяемое свойство,
// ради которого книги советуют Leiden.
func TestSplitLeavesNothingDisconnected(t *testing.T) {
	adj, list := brokenTheme()
	out, _ := splitDisconnected(adj, list)

	for _, com := range out {
		if parts := len(components(adj, com.Members)); parts != 1 {
			t.Fatalf("тема %d осталась из %d частей", com.ID, parts)
		}
	}
}
