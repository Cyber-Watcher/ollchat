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
	// Мелкая получает новый номер — следующий свободный номер темы — и идёт
	// на описание заново.
	if small.ID != 2 {
		t.Errorf("номер новой темы %d, ожидался 2 (следующий свободный)", small.ID)
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

// Номер отрезанной части не совпадает с номером чужой темы.
//
// До 07.10.2026 части давался номер её наименьшего ПОНЯТИЯ, а номера тем
// и понятий — разные пространства: здесь понятие №8 совпадало с темой №8,
// и graph_topic #8 отдавал не ту тему (аудит, 4.5).
func TestSplitPartIDsDoNotCollide(t *testing.T) {
	adj, list := brokenTheme()
	adj[20] = map[uint32]float64{21: 5}
	adj[21] = map[uint32]float64{20: 5}
	list = append(list,
		Community{ID: 8, Level: 0, Parent: 7, Members: []uint32{20, 21}, Title: "чужая"},
		Community{ID: 7, Level: 1, Parent: -1, Members: []uint32{1, 2, 3, 8, 9, 20, 21}})

	out, _ := splitDisconnected(adj, list)
	seen := map[int]bool{}
	for _, com := range out {
		if seen[com.ID] {
			t.Fatalf("номер темы %d выдан дважды: %+v", com.ID, out)
		}
		seen[com.ID] = true
	}
	c := &Communities{List: out}
	if got, ok := c.Get(8); !ok || got.Title != "чужая" {
		t.Fatalf("тема №8 подменена отрезанной частью: %+v", got)
	}
}

// Разбиение, записанное с повторяющимися номерами, читается, и номера при
// чтении становятся различными: верхние темы и первая из повторов номер
// сохраняют, ссылки Parent остаются верными.
func TestLoadCommunitiesMakesIDsUnique(t *testing.T) {
	g, _ := graph(t)
	c := &Communities{List: []Community{
		{ID: 0, Level: 0, Parent: 9, Members: []uint32{1, 2}, Title: "первая"},
		{ID: 9, Level: 0, Parent: 9, Members: []uint32{3, 4}, Title: "часть под номером верхней"},
		{ID: 4, Level: 0, Parent: 9, Members: []uint32{5, 6}, Title: "своя"},
		{ID: 4, Level: 0, Parent: 9, Members: []uint32{7, 8}, Title: "часть под чужим номером"},
		{ID: 9, Level: 1, Parent: -1, Members: []uint32{1, 2, 3, 4, 5, 6, 7, 8}, Title: "верхняя"},
	}}
	must(t, g.saveCommunities(c))
	got, err := g.LoadCommunities()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, com := range got.List {
		if seen[com.ID] {
			t.Fatalf("после чтения номер %d повторяется: %+v", com.ID, got.List)
		}
		seen[com.ID] = true
	}
	for id, title := range map[int]string{0: "первая", 4: "своя", 9: "верхняя"} {
		if com, ok := got.Get(id); !ok || com.Title != title {
			t.Errorf("тема №%d: %q, ожидалась %q", id, com.Title, title)
		}
	}
	for _, com := range got.List {
		if com.Level == 0 && com.Parent != 9 {
			t.Errorf("ссылка на верхнюю тему сбита: %+v", com)
		}
	}
}
