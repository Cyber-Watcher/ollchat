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

	// Ниже — что отсёк порог веса (0 у опыта без порога).
	CutPairs int // пар связей, не дотянувших до порога
	CutNodes int // понятий, оставшихся после отсечения без единой связи
}

// PartitionOpts — условия опыта с разбиением.
type PartitionOpts struct {
	// Weights — множитель веса для вида связи; 0 исключает вид вовсе.
	Weights map[uint8]float64
	// Resolution — та же, что у рабочего разбиения; 0 — умолчание.
	Resolution float64

	// MinWeight — порог веса ПАРЫ понятий: связи с меньшим весом в разбиении
	// не участвуют. Вес пары — сумма подтверждений, поэтому MinWeight = 2
	// означает «связь, встреченная только в одном куске, тему не образует».
	//
	// **Откуда.** «Neo4j: The Definitive Guide» (2025, стр. 368) перед поиском
	// сообществ строит отдельный граф со-встречаемости с порогом
	// (`WHERE tracksInCommon >= 4`), а не считает темы по всем совпадениям.
	// У нас 63.5% различных связей стоят на одном подтверждении (замер
	// 08.09.2026) и весят в Louvain столько же, сколько подтверждённые десятью
	// кусками. Порог отсекает шум — и вместе с ним редкие понятия, поэтому
	// это опыт с замером, а не правка по книге (этап 101, Г2).
	MinWeight float64

	// OnceFactor — множитель веса для пар, подтверждённых ровно один раз:
	// мягкая замена порогу. 0 — не трогать.
	//
	// Нужен потому, что порог оказался разрушительным: на графе books
	// w ≥ 2 срезает 72% пар и оставляет без связей 143 тысячи понятий
	// из 217 (замер 09.09.2026). Ослабление — тот же приём, что дал Ф1
	// на «связано»: вес 0.5 вместо исключения сохранил понятия и почти
	// весь выигрыш.
	OnceFactor float64
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
	return g.ExperimentPartitionWith(PartitionOpts{Weights: weights, Resolution: resolution})
}

// ExperimentPartitionWith — то же с полным набором условий, включая порог веса.
func (g *Graph) ExperimentPartitionWith(o PartitionOpts) PartitionExperiment {
	weights, resolution := o.Weights, o.Resolution

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
	// Порог применяется к сумме подтверждений пары, а не к отдельной записи:
	// связь, встреченная в трёх кусках, — это три записи весом 1, и отсекать
	// их поодиночке значило бы отсечь её целиком.
	if o.OnceFactor > 0 {
		weakenOnce(adj, o.OnceFactor)
	}
	cutPairs, cutNodes := cutWeak(adj, o.MinWeight)

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
	out := PartitionExperiment{Nodes: len(order), Edges: edges, Themes: len(sizes),
		CutPairs: cutPairs, CutNodes: cutNodes}
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

// cutWeak убирает из матрицы смежности пары легче порога, а следом — понятия,
// оставшиеся вовсе без связей. Возвращает, сколько пар и понятий отсечено.
//
// Порог считается по весу пары (сумме подтверждений), обе стороны удаляются
// вместе: односторонний остаток сделал бы граф несимметричным, и Louvain
// посчитал бы по нему разные степени у двух концов одной связи.
func cutWeak(adj map[uint32]map[uint32]float64, min float64) (pairs, nodes int) {
	if min <= 0 {
		return 0, 0
	}
	for a, nbs := range adj {
		for b, w := range nbs {
			if w < min {
				delete(nbs, b)
				if a < b {
					pairs++
				}
			}
		}
	}
	for a, nbs := range adj {
		if len(nbs) == 0 {
			delete(adj, a)
			nodes++
		}
	}
	return pairs, nodes
}

// weakenOnce умножает на k вес пар, подтверждённых ровно один раз.
//
// Обе стороны правятся вместе — по той же причине, что и в cutWeak: половина
// правки сделала бы граф несимметричным.
func weakenOnce(adj map[uint32]map[uint32]float64, k float64) int {
	if k <= 0 || k >= 1 {
		return 0
	}
	n := 0
	for a, nbs := range adj {
		for b, w := range nbs {
			if w == 1 {
				nbs[b] = k
				if a < b {
					n++
				}
			}
		}
	}
	return n
}
