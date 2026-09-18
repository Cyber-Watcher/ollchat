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

	// Ниже — что сделали порог и ослабление (0 у опыта без них).
	CutPairs      int // пар связей, не дотянувших до порога
	CutNodes      int // понятий, оставшихся после отсечения без единой связи
	WeakenedPairs int // пар с одним подтверждением, ослабленных OnceFactor

	// Assign — понятие → номер сообщества; только при KeepAssignment.
	Assign map[uint32]uint32

	// Modularity — модулярность разметки (с тем же γ); у Лейдена — нижнего
	// уровня, ModularityTop — верхнего (с ним и сравнивать Лувен: нижний
	// уровень мельче по построению). Disconnected — сколько тем несвязны
	// внутри себя (Лейден обещает ноль, Лувен — не обещает).
	Modularity    float64
	ModularityTop float64
	Disconnected  int
	// LevelThemes — тем на каждом уровне иерархии Лейдена, снизу вверх;
	// у Лувена — один уровень. Levels — разметки по уровням при KeepAssignment.
	LevelThemes []int
	Levels      []map[uint32]uint32
}

// PartitionOpts — условия опыта с разбиением.
type PartitionOpts struct {
	// Weights — множитель веса для вида связи; 0 исключает вид вовсе.
	Weights map[uint8]float64
	// Resolution — та же, что у рабочего разбиения; 0 — умолчание.
	Resolution float64

	// MinWeight — порог на число подтверждений ПАРЫ понятий: пары с меньшим
	// числом в разбиении не участвуют. MinWeight = 2 означает «связь,
	// встреченная только в одном куске, тему не образует».
	//
	// **Откуда.** «Neo4j: The Definitive Guide» (2025, стр. 368) перед поиском
	// сообществ строит отдельный граф со-встречаемости с порогом
	// (`WHERE tracksInCommon >= 4`), а не считает темы по всем совпадениям.
	// У нас 63.5% различных связей стоят на одном подтверждении (замер
	// 08.09.2026) и весят в Louvain столько же, сколько подтверждённые десятью
	// кусками. Порог отсекает шум — и вместе с ним редкие понятия, поэтому
	// это опыт с замером, а не правка по книге (этап 101, Г2).
	MinWeight float64

	// ByOrigins — считать вес пары по ИСТОЧНИКАМ, а не по кускам: соседние
	// куски одной книги (номера подряд) дают одно подтверждение, а не два
	// (этап 101, Г9: 96% таких пар — копия фразы из зоны перекрытия). Порог
	// и OnceFactor при этом смотрят на число источников.
	ByOrigins bool

	// KeepAssignment — вернуть разбиение по узлам (Assign), а не только размеры:
	// нужно, чтобы сравнить опытное разбиение с рабочим по составу тем.
	KeepAssignment bool

	// Leiden — разбивать Лейденом (leiden.go) вместо Лувена: уточнение даёт
	// связные темы и иерархию уровней; сравнение — этап 90, третий граф.
	Leiden bool

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

// ExperimentPartition считает разбиение при заданных условиях, ничего не записывая.
//
// Weights — множитель веса для вида связи: 0 исключает вид вовсе, 0.5 делает
// его вдвое менее важным, отсутствие вида в карте означает «как есть».
// Промежуточный вариант нужен потому, что исключение — не единственный выбор:
// связь «связано» слабее прочих, но у 19% понятий она единственная, и выбросив
// её, мы выбрасываем сами понятия.
//
// Порог MinWeight и множитель OnceFactor смотрят на ЧИСЛО ПОДТВЕРЖДЕНИЙ пары,
// а не на её вес после множителей видов: одиночное «связано» при множителе 0.5
// весит 0.5, а два «связано» — 1.0, и решать по весу, «одиночная ли пара»,
// значило бы перепутать их местами (ревизия 10.09.2026).
func (g *Graph) ExperimentPartition(o PartitionOpts) PartitionExperiment {
	weights, resolution := o.Weights, o.Resolution

	adj := map[uint32]map[uint32]float64{}
	conf := map[uint32]map[uint32]int{} // подтверждений у пары, зеркально
	add := func(a, b uint32, w float64) {
		if adj[a] == nil {
			adj[a] = map[uint32]float64{}
			conf[a] = map[uint32]int{}
		}
		adj[a][b] += w
		conf[a][b]++
	}
	edges := 0
	// По источникам: записи одной пары группируются по книге, и соседние куски
	// схлопываются в один источник; вес источника — вес его первой записи.
	type pk struct{ a, b uint32 }
	type rec = originRec
	byPair := map[pk]map[uint32][]rec{}
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
			if !o.ByOrigins {
				add(ed.Src, ed.Dst, w)
				add(ed.Dst, ed.Src, w)
				edges++
				continue
			}
			k := pk{ed.Src, ed.Dst}
			if k.a > k.b {
				k.a, k.b = k.b, k.a
			}
			if byPair[k] == nil {
				byPair[k] = map[uint32][]rec{}
			}
			byPair[k][ed.Evidence.Doc] = append(byPair[k][ed.Evidence.Doc], rec{ed.Evidence.Ord, w})
			edges++
		}
	}
	for k, docs := range byPair {
		for _, recs := range docs {
			for _, w := range originWeights(recs) {
				add(k.a, k.b, w)
				add(k.b, k.a, w)
				// conf считает источники, а не записи: add увеличил его на запись,
				// и это верно — здесь каждая запись и есть источник.
			}
		}
	}
	out := PartitionExperiment{Edges: edges}
	if o.OnceFactor > 0 {
		out.WeakenedPairs = weakenOnce(adj, conf, o.OnceFactor)
	}
	out.CutPairs, out.CutNodes = cutWeak(adj, conf, o.MinWeight)

	order := make([]uint32, 0, len(adj))
	for id := range adj {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	if resolution <= 0 {
		resolution = DefaultResolution
	}
	var comm map[uint32]uint32
	if o.Leiden {
		lr := leiden(adj, order, resolution)
		comm = lr.Bottom
		out.Modularity = modularity(adj, order, lr.Bottom, resolution)
		out.ModularityTop = lr.Modularity
		for lvl := 0; lvl < lr.Levels(); lvl++ {
			at := lr.At(lvl)
			out.LevelThemes = append(out.LevelThemes, distinctCount(at))
			if o.KeepAssignment {
				out.Levels = append(out.Levels, at)
			}
		}
	} else {
		comm = louvain(adj, order, resolution)
		out.Modularity = modularity(adj, order, comm, resolution)
		out.ModularityTop = out.Modularity
		out.LevelThemes = []int{distinctCount(comm)}
	}
	out.Disconnected = disconnectedCommunities(adj, order, comm)

	sizes := map[uint32]int{}
	for _, c := range comm {
		sizes[c]++
	}
	out.Nodes, out.Themes = len(order), len(sizes)
	if o.KeepAssignment {
		out.Assign = comm
	}
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

// cutWeak убирает из матрицы смежности пары с числом подтверждений меньше
// порога, а следом — понятия, оставшиеся вовсе без связей. Возвращает,
// сколько пар и понятий отсечено.
//
// Обе стороны удаляются вместе: односторонний остаток сделал бы граф
// несимметричным, и Louvain посчитал бы по нему разные степени у двух концов
// одной связи.
func cutWeak(adj map[uint32]map[uint32]float64, conf map[uint32]map[uint32]int, min float64) (pairs, nodes int) {
	if min <= 0 {
		return 0, 0
	}
	for a, nbs := range adj {
		for b := range nbs {
			if float64(conf[a][b]) < min {
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
// Возвращает, сколько пар ослаблено.
//
// Обе стороны правятся вместе — по той же причине, что и в cutWeak: половина
// правки сделала бы граф несимметричным.
func weakenOnce(adj map[uint32]map[uint32]float64, conf map[uint32]map[uint32]int, k float64) int {
	if k <= 0 || k >= 1 {
		return 0
	}
	n := 0
	for a, nbs := range adj {
		for b, w := range nbs {
			if conf[a][b] == 1 {
				nbs[b] = w * k
				if a < b {
					n++
				}
			}
		}
	}
	return n
}

// distinctCount — сколько разных сообществ в разметке.
func distinctCount(m map[uint32]uint32) int {
	seen := make(map[uint32]bool, len(m))
	for _, c := range m {
		seen[c] = true
	}
	return len(seen)
}
