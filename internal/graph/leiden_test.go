package graph

import (
	"fmt"
	"reflect"
	"testing"
)

// Два явных сгустка — Лейден находит их нижним уровнем, модулярность
// положительна, каждое сообщество связно.
func TestLeidenFindsObviousClusters(t *testing.T) {
	adj, order := twoClusters()
	res := leiden(adj, order, 1)
	if res.Bottom[1] != res.Bottom[2] || res.Bottom[2] != res.Bottom[3] {
		t.Errorf("первый сгусток разорван: %v", res.Bottom)
	}
	if res.Bottom[4] != res.Bottom[5] || res.Bottom[5] != res.Bottom[6] {
		t.Errorf("второй сгусток разорван: %v", res.Bottom)
	}
	if res.Bottom[1] == res.Bottom[4] {
		t.Errorf("сгустки слиты: %v", res.Bottom)
	}
	if res.Modularity <= 0 {
		t.Errorf("модулярность %.3f, ожидалась положительная", res.Modularity)
	}
	if n := disconnectedCommunities(adj, order, res.Bottom); n != 0 {
		t.Errorf("несвязных сообществ %d", n)
	}
}

// pseudoGraph — детерминированный «случайный» граф: k сгустков по n узлов,
// внутри плотно, между сгустками редкие связи.
func pseudoGraph(k, n int) (map[uint32]map[uint32]float64, []uint32) {
	adj := map[uint32]map[uint32]float64{}
	link := func(a, b uint32, w float64) {
		if a == b {
			return
		}
		if adj[a] == nil {
			adj[a] = map[uint32]float64{}
		}
		if adj[b] == nil {
			adj[b] = map[uint32]float64{}
		}
		adj[a][b] += w
		adj[b][a] += w
	}
	seed := uint32(12345)
	rnd := func() uint32 { seed = seed*1664525 + 1013904223; return seed >> 8 }
	var order []uint32
	for c := 0; c < k; c++ {
		for i := 0; i < n; i++ {
			id := uint32(c*n + i + 1)
			order = append(order, id)
			for j := 0; j < 3; j++ { // три связи внутри сгустка
				link(id, uint32(c*n+int(rnd()%uint32(n))+1), 1)
			}
			if rnd()%7 == 0 { // изредка — наружу
				link(id, uint32(rnd()%uint32(k*n))+1, 1)
			}
		}
	}
	return adj, order
}

// Свойство Лейдена: сообщества связны на всех уровнях; разметка детерминирована.
func TestLeidenCommunitiesAreConnectedAndDeterministic(t *testing.T) {
	adj, order := pseudoGraph(12, 40)
	res := leiden(adj, order, 1)
	if n := disconnectedCommunities(adj, order, res.Bottom); n != 0 {
		t.Errorf("нижний уровень: несвязных сообществ %d", n)
	}
	if n := disconnectedCommunities(adj, order, res.Top()); n != 0 {
		t.Errorf("верхний уровень: несвязных сообществ %d", n)
	}
	again := leiden(adj, order, 1)
	if !reflect.DeepEqual(res.Bottom, again.Bottom) || !reflect.DeepEqual(res.Top(), again.Top()) {
		t.Error("два запуска на одних данных дали разные разметки")
	}
	t.Logf("узлов %d, кругов %d, уровней %d, тем внизу %d, наверху %d, Q=%.3f",
		len(order), res.Rounds, res.Levels(), distinct(res.Bottom), distinct(res.Top()), res.Modularity)
	// Лувен на том же графе: связность не гарантирована, и сравнение
	// модулярности — хотя бы не хуже.
	lq := modularity(adj, order, louvain(adj, order, 1), 1)
	if res.Modularity+0.05 < lq {
		t.Errorf("модулярность Лейдена %.3f заметно ниже Лувена %.3f", res.Modularity, lq)
	}
}

// Иерархия: восемь сгустков, попарно связанных сильнее, чем с остальными, —
// нижний уровень видит восемь тем, верхний сводит их в четыре пары.
func TestLeidenBuildsHierarchy(t *testing.T) {
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
	var order []uint32
	const size = 6
	for c := uint32(0); c < 8; c++ {
		base := c*size + 1
		for i := uint32(0); i < size; i++ {
			order = append(order, base+i)
			for j := i + 1; j < size; j++ {
				link(base+i, base+j, 10) // плотный сгусток
			}
		}
	}
	// Пары сгустков: три связи по 15. Одному узлу переезжать невыгодно
	// (внутри у него 50 против 15 снаружи), а слияние сгустков целиком
	// модулярность поднимает (45 против ожидаемых ~36) — это и есть
	// второй уровень: он виден только на свёрнутом графе.
	for c := uint32(0); c < 8; c += 2 {
		for i := uint32(0); i < 3; i++ {
			link(c*size+1+i, (c+1)*size+1+i, 15)
		}
	}
	for c := uint32(0); c < 8; c++ { // между парами — по одной слабой
		link(c*size+1, ((c+2)%8)*size+1, 0.5)
	}
	res := leiden(adj, order, 1)
	if got := distinct(res.Bottom); got != 8 {
		t.Errorf("тем нижнего уровня %d, ожидалось 8", got)
	}
	if res.Levels() < 2 {
		t.Fatalf("иерархии нет: уровней %d, кругов %d", res.Levels(), res.Rounds)
	}
	if got := distinct(res.Top()); got != 4 {
		t.Errorf("тем верхнего уровня %d, ожидалось 4: %v", got, res.Top())
	}
	for id := uint32(1); id <= size; id++ { // первый сгусток и второй — одна верхняя тема
		if res.Top()[id] != res.Top()[id+size] {
			t.Errorf("узлы %d и %d разошлись по верхним темам", id, id+size)
		}
	}
	if n := disconnectedCommunities(adj, order, res.Top()); n != 0 {
		t.Errorf("верхний уровень: несвязных %d", n)
	}
}

func distinct(m map[uint32]uint32) int {
	seen := map[uint32]bool{}
	for _, c := range m {
		seen[c] = true
	}
	return len(seen)
}

// Лейден как алгоритм разбиения графа: темы связны, объединения указаны,
// алгоритм записан в разбиении; Лувен по умолчанию не тронут.
func TestBuildCommunitiesWithLeiden(t *testing.T) {
	g, err := Create(collection(t), "books", 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	// Восемь плотных сгустков по 6 понятий, пары сгустков связаны сильнее.
	const size, groups = 6, 8
	for c := 0; c < groups; c++ {
		for i := 0; i < size; i++ {
			if _, _, err := g.Entities().Add(fmt.Sprintf("понятие-%d-%d", c, i), TypeConcept); err != nil {
				t.Fatal(err)
			}
		}
	}
	id := func(c, i int) uint32 { return uint32(c*size + i + 1) }
	add := func(a, b uint32, w int) {
		for k := 0; k < w; k++ {
			if err := g.Edges().Add(Edge{Src: a, Dst: b, Type: RelUses, Weight: 1,
				Evidence: ChunkKey{Doc: 1, Ord: uint32(k*1000 + int(a)*10 + int(b))}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for c := 0; c < groups; c++ {
		for i := 0; i < size; i++ {
			for j := i + 1; j < size; j++ {
				add(id(c, i), id(c, j), 10)
			}
		}
	}
	for c := 0; c < groups; c += 2 {
		for i := 0; i < 3; i++ {
			add(id(c, i), id(c+1, i), 15)
		}
	}
	res, err := g.PartitionOnly(CommunityOpts{Algorithm: AlgoLeiden, Resolution: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Algorithm != AlgoLeiden || res.LeidenLevels < 2 {
		t.Fatalf("алгоритм %q, уровней %d — ожидался leiden с лестницей", res.Algorithm, res.LeidenLevels)
	}
	if n := len(res.Level(0)); n != groups {
		t.Errorf("тем нижнего уровня %d, ожидалось %d", n, groups)
	}
	if n := len(res.Level(1)); n != groups/2 {
		t.Errorf("объединений %d, ожидалось %d", n, groups/2)
	}
	for _, com := range res.Level(0) {
		if com.Parent < 0 {
			t.Errorf("тема %d без объединения", com.ID)
		}
	}
	// Умолчание — Лувен, алгоритм так и записывается.
	def, err := g.PartitionOnly(CommunityOpts{Resolution: 1})
	if err != nil {
		t.Fatal(err)
	}
	if def.Algorithm != AlgoLouvain {
		t.Errorf("умолчание записано как %q, ожидалось louvain", def.Algorithm)
	}
	if !KnownAlgorithm("") || !KnownAlgorithm("leiden") || KnownAlgorithm("kmeans") {
		t.Error("KnownAlgorithm путает имена")
	}
}
