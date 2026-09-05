package graph

import (
	"testing"
)

// twoClusters — граф из двух явных сгустков, связанных одним слабым ребром.
// Правильное разбиение очевидно глазами, и алгоритм обязан его найти.
func twoClusters() (map[uint32]map[uint32]float64, []uint32) {
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
	// Сгусток 1: 1-2-3 плотно.
	link(1, 2, 10)
	link(2, 3, 10)
	link(1, 3, 10)
	// Сгусток 2: 4-5-6 плотно.
	link(4, 5, 10)
	link(5, 6, 10)
	link(4, 6, 10)
	// Мостик между ними — слабый.
	link(3, 4, 1)
	return adj, []uint32{1, 2, 3, 4, 5, 6}
}

func TestLouvainFindsObviousClusters(t *testing.T) {
	adj, order := twoClusters()
	comm := louvain(adj, order, 1)

	if comm[1] != comm[2] || comm[2] != comm[3] {
		t.Errorf("первый сгусток разорван: %v", comm)
	}
	if comm[4] != comm[5] || comm[5] != comm[6] {
		t.Errorf("второй сгусток разорван: %v", comm)
	}
	if comm[1] == comm[4] {
		t.Errorf("сгустки слиты в одно сообщество: %v", comm)
	}
}

// Разбиение обязано быть одинаковым от запуска к запуску: иначе резюме
// сообществ меняются сами собой, и объяснить это нечем.
func TestLouvainIsDeterministic(t *testing.T) {
	adj, order := twoClusters()
	first := louvain(adj, order, 1)
	for i := 0; i < 20; i++ {
		again := louvain(adj, order, 1)
		for _, id := range order {
			if first[id] != again[id] {
				t.Fatalf("запуск %d дал другое разбиение: узел %d был в %d, стал в %d",
					i, id, first[id], again[id])
			}
		}
	}
}

// Номера сообществ идут подряд с нуля, в порядке первого появления узлов.
func TestLouvainRenumbersFromZero(t *testing.T) {
	adj, order := twoClusters()
	comm := louvain(adj, order, 1)
	seen := map[uint32]bool{}
	for _, id := range order {
		seen[comm[id]] = true
	}
	for n := uint32(0); n < uint32(len(seen)); n++ {
		if !seen[n] {
			t.Errorf("номер %d пропущен, номера должны идти подряд: %v", n, comm)
		}
	}
}

// Свёртка складывает веса связей между сообществами и выбрасывает связи
// внутри них: на верхнем уровне важна только связность тем между собой.
func TestRollUpAggregates(t *testing.T) {
	adj, order := twoClusters()
	comm := louvain(adj, order, 1)
	up, ids := rollUp(adj, order, comm)

	if len(ids) != 2 {
		t.Fatalf("свёрнутых узлов %d, ожидалось два: %v", len(ids), ids)
	}
	a, b := ids[0], ids[1]
	if got := up[a][b]; got != 1 {
		t.Errorf("вес мостика между сгустками %v, ожидался 1", got)
	}
	if _, ok := up[a][a]; ok {
		t.Error("связь сообщества с самим собой не должна попадать в свёртку")
	}
}

// Вес внутри группы считается один раз на связь, а не дважды.
func TestInnerCountsEdgeOnce(t *testing.T) {
	adj, _ := twoClusters()
	if got := inner(adj, []uint32{1, 2, 3}); got != 30 {
		t.Errorf("вес внутри первого сгустка %v, ожидалось 30 (три связи по 10)", got)
	}
}

// Номера сообществ не должны совпадать между уровнями: обзор ищет тему
// по номеру, и совпадение подсовывает объединение вместо мелкого сообщества.
// Поймано на живом графе — тема на 1 715 понятий при пределе в 200.
func TestCommunityIDsDoNotCollideAcrossLevels(t *testing.T) {
	adj, order := twoClusters()
	small := louvain(adj, order, 1)
	rolled, rolledOrder := rollUp(adj, order, small)
	big := louvain(rolled, rolledOrder, 1)

	g := &Graph{}
	list := g.assembleFor(adj, order, small, big, map[uint32]int{})

	lvl0, lvl1 := map[int]bool{}, map[int]bool{}
	for _, c := range list {
		switch c.Level {
		case 0:
			lvl0[c.ID] = true
		case 1:
			lvl1[c.ID] = true
		}
	}
	for id := range lvl1 {
		if lvl0[id] {
			t.Errorf("номер %d занят и мелким сообществом, и объединением", id)
		}
	}
	// Ссылка вверх должна указывать на существующее объединение.
	for _, c := range list {
		if c.Level == 0 && c.Parent >= 0 && !lvl1[c.Parent] {
			t.Errorf("сообщество %d ссылается на объединение %d, которого нет", c.ID, c.Parent)
		}
	}
}
