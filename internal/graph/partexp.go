package graph

import "sort"

// Опыт с разбиением: посчитать темы на подмножестве связей, ничего не меняя.
//
// **Откуда взялось.** «Graph-Powered Machine Learning» (Negro, 2021, стр. 451):
// автор строит отдельный «виртуальный граф» только из нужных связей и запускает
// на нём поиск сообществ, «ignoring all the rest». У нас разбиение считается по
// всему графу разом, включая нетипизированное «связано» — а его треть, и это
// самый шумный вид: он ставится, когда модель не смогла назвать связь точнее.
//
// Функция ничего не записывает: она отвечает на вопрос «а что было бы, если»,
// и ответ нужен до того, как трогать рабочее разбиение.

// PartitionExperiment — что получилось на подмножестве связей.
type PartitionExperiment struct {
	Nodes     int   // понятий, у которых остались связи
	Edges     int   // связей (в одну сторону)
	Themes    int   // сколько получилось тем
	Singleton int   // тем из одного понятия
	Largest   int   // размер самой большой темы
	Median    int   // медианный размер темы
	Sizes     []int // размеры по убыванию, первые двадцать
}

// ExperimentPartition считает разбиение с заданными весами видов связей.
//
// weights — множитель веса для вида связи: 0 исключает вид вовсе, 0.5 делает
// его вдвое менее важным, отсутствие вида в карте означает «как есть».
// Промежуточный вариант нужен потому, что исключение — не единственный выбор:
// связь «связано» слабее прочих, но у 19% понятий она единственная, и выбросив
// её, мы выбрасываем сами понятия.
//
// resolution — та же, что у рабочего разбиения; 0 — умолчание.
func (g *Graph) ExperimentPartition(weights map[uint8]float64, resolution float64) PartitionExperiment {

	adj := map[uint32]map[uint32]float64{}
	add := func(a, b uint32, w float64) {
		if adj[a] == nil {
			adj[a] = map[uint32]float64{}
		}
		adj[a][b] += w
	}
	edges := 0
	for _, ent := range g.Entities().Live() {
		for _, ed := range g.Edges().Of(ent.ID) {
			w := float64(ed.Weight)
			if w <= 0 {
				w = 1
			}
			if k, ok := weights[ed.Type]; ok {
				if k <= 0 {
					continue
				}
				w *= k
			}
			add(ed.Src, ed.Dst, w)
			add(ed.Dst, ed.Src, w)
			edges++
		}
	}
	order := make([]uint32, 0, len(adj))
	for id := range adj {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	if resolution <= 0 {
		resolution = DefaultResolution
	}
	comm := louvain(adj, order, resolution)

	sizes := map[uint32]int{}
	for _, c := range comm {
		sizes[c]++
	}
	out := PartitionExperiment{Nodes: len(order), Edges: edges, Themes: len(sizes)}
	all := make([]int, 0, len(sizes))
	for _, n := range sizes {
		all = append(all, n)
		if n == 1 {
			out.Singleton++
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(all)))
	if len(all) > 0 {
		out.Largest = all[0]
		out.Median = all[len(all)/2]
	}
	if len(all) > 20 {
		out.Sizes = all[:20]
	} else {
		out.Sizes = all
	}
	return out
}
