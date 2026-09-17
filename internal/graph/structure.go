package graph

// Строение графа в числах: связность целиком, одиночки, опора связей.
//
// **Откуда взялось.** «Knowledge Graphs and LLMs in Action» (Negro, 2025, стр. 124)
// называет мерами качества графа плотность, проводимость и относительный размер
// наибольшей связной компоненты. Доктор до сих пор мерил связность **тем**
// (`connectivity.go`), а сам граф — нет: рассыпался он на острова или держится
// одним куском, было не видно.
//
// Второе число — доля связей, стоящих на одном подтверждении. Замер 08.09.2026
// по готовому графу дал 63.5% из 803 тысяч различных связей, и это
// прямо влияет на разбиение: в Louvain такая связь весит столько же, сколько
// подтверждённая десятью кусками. Чтобы решать, ставить ли порог (этап 101, Г2),
// число надо видеть в докторе, а не запускать отдельный инструмент.
//
// Всё считается обходом в памяти по уже открытому графу: карта не нужна,
// модель не участвует.

// Structure — строение графа в числах.
type Structure struct {
	// Nodes — понятий, у которых есть хоть одна связь.
	Nodes int
	// Isolated — живых понятий без единой связи: в темы они не попадают
	// никогда, потому что разбиение считается по связям.
	Isolated int

	// Pairs — различных пар понятий, между которыми есть связь (направление
	// не различается: A→B и B→A — одна пара).
	Pairs int
	// PairsOnce — из них подтверждённых ровно один раз.
	PairsOnce int

	// Parts — на сколько несвязных частей распадается граф.
	Parts int
	// Largest — размер наибольшей части в понятиях.
	Largest int

	// HubLimit — порог, с которого понятие считается хабом (из правил графа),
	// и Hubs — сколько понятий его перешагнуло.
	//
	// Порог у нас абсолютный (500 связей), а книга определяет хаб верхушкой
	// распределения: «Knowledge Graphs and LLMs in Action» (2025, стр. 226–227)
	// берёт топ-350 по степени. Замер 09.09.2026: наши 500 связей — это ровно
	// 200 понятий, верхние 0.088%, то есть порог строже книжного и менять его
	// не на что. Но граф растёт, и доля поплывёт; чтобы это заметить, число
	// должно быть на виду у доктора, а не в отдельном инструменте (этап 101, Г3).
	HubLimit int
	Hubs     int
}

// HubShare — какую долю понятий со связями занимают хабы, в процентах
// (дробных: хабов сотни на сотни тысяч понятий).
func (s Structure) HubShare() float64 {
	if s.Nodes == 0 {
		return 0
	}
	return 100 * float64(s.Hubs) / float64(s.Nodes)
}

// OnceShare — доля связей на одном подтверждении, в процентах.
func (s Structure) OnceShare() int {
	if s.Pairs == 0 {
		return 0
	}
	return 100 * s.PairsOnce / s.Pairs
}

// LargestShare — какую долю понятий со связями занимает наибольшая часть,
// в процентах. Это и есть «relative size of largest connected component».
func (s Structure) LargestShare() int {
	if s.Nodes == 0 {
		return 0
	}
	return 100 * s.Largest / s.Nodes
}

// IsolatedShare — доля одиночек среди живых понятий, в процентах.
func (s Structure) IsolatedShare() int {
	total := s.Nodes + s.Isolated
	if total == 0 {
		return 0
	}
	return 100 * s.Isolated / total
}

// Structure меряет строение графа: связность целиком, одиночек и опору связей.
func (g *Graph) Structure() Structure {
	st, _ := g.Shape(nil)
	return st
}

// neighborCount — сколько соседей у понятия по правилу поиска.
func (g *Graph) neighborCount(id uint32) int { return len(g.edge.Neighbors(id)) }

// structure — та же работа над готовой матрицей смежности.
//
// live — сколько живых понятий в реестре всего; одиночки считаются вычитанием,
// потому что перебирать реестр второй раз незачем: понятие либо есть в adj,
// либо связей у него нет вовсе.
func structure(adj map[uint32]map[uint32]float64, order []uint32, live, hubLimit int) Structure {
	out := Structure{Nodes: len(order), HubLimit: hubLimit}
	if live > out.Nodes {
		out.Isolated = live - out.Nodes
	}

	// Пары считаются по одному разу: обе стороны лежат в adj зеркально.
	for _, id := range order {
		for nb, w := range adj[id] {
			if nb <= id {
				continue
			}
			out.Pairs++
			if w <= 1 {
				out.PairsOnce++
			}
		}
	}

	for _, part := range components(adj, order) {
		out.Parts++
		if len(part) > out.Largest {
			out.Largest = len(part)
		}
	}
	return out
}

// countHubs считает понятия, перешагнувшие порог хаба, ТЕМ ЖЕ правилом,
// каким запрет пользуется в поиске: по числу соседей с учётом направления
// (`Edges.Neighbors`), а не по неориентированной смежности.
//
// Разница не косметическая: сосед, с которым понятие связано в обе стороны,
// в правиле считается дважды, и по adj доктор насчитал 160 хабов там, где
// поиск отсекает 200 (замер 09.09.2026). Число в докторе, расходящееся
// с числом в правиле, хуже отсутствия числа.
//
// Считается только для кандидатов: у кого уникальных соседей меньше половины
// порога, тот не дотянет и с удвоением, а `Neighbors` — не бесплатный вызов.
func countHubs(adj map[uint32]map[uint32]float64, order []uint32, limit int,
	neighbors func(uint32) int) int {

	if limit <= 0 || neighbors == nil {
		return 0
	}
	n := 0
	for _, id := range order {
		if len(adj[id])*2 < limit {
			continue
		}
		if neighbors(id) >= limit {
			n++
		}
	}
	return n
}

// Shape меряет строение графа и связность тем за один обход.
//
// Обе меры считаются по одной и той же матрице смежности, а её построение —
// самая дорогая часть (на графе books это миллионы связей). Раздельные вызовы
// `Structure` и `CommunityConnectivity` строили бы её дважды.
func (g *Graph) Shape(c *Communities) (Structure, Connectivity) {
	adj, order := g.undirected()
	st := structure(adj, order, len(g.Entities().Live()), g.rules.ChainHubLimit)
	// «На одном подтверждении» — по числу кусков-источников пары, а не по весу:
	// «связано» весит 0,5 (Rules.RelatedWeight), и пара с двумя такими записями
	// при правиле `w <= 1` считалась одиночной. Замер 17.09.2026 на выборке
	// 4 000 пар: одиночных 72,5%, а доктор показывал 83%.
	st.PairsOnce = g.Corroboration().Single
	st.Hubs = countHubs(adj, order, st.HubLimit, g.neighborCount)
	var conn Connectivity
	if c != nil {
		conn = connectivity(adj, c)
	}
	return st, conn
}
