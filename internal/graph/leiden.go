package graph

import "sort"

// Leiden: Лувен с уточнением и настоящей иерархией (Traag, Waltman, van Eck, 2019).
//
// Чем отличается от louvain.go. Лувен у нас — одна фаза перемещений плюс
// дробление крупных сообществ и один свёрнутый уровень. Лейден идёт кругами:
//
//  1. перемещения — те же, что у Лувена (moveNodes), но начиная с разметки
//     прошлого круга, а не с одиночек;
//  2. уточнение — внутри каждого сообщества разметка собирается заново
//     из одиночек: узел присоединяется только к соседям по связи и только
//     при положительном приросте модулярности. Отсюда главное свойство
//     Лейдена: **уточнённое сообщество связно по построению** — узел без
//     единой связи с группой в неё не попадёт. У Лувена сообщество может
//     развалиться на несвязные куски, когда узел-мост переезжает
//     («Domain-Specific Small Language Models», 2026, стр. 321; у нас это
//     лечится задним числом — splitDisconnected);
//  3. свёртка — уточнённые сообщества становятся узлами (внутренний вес —
//     петлёй), разметка нового графа берётся из шага 1, и круг повторяется,
//     пока перемещения что-то меняют.
//
// Круги и дают иерархию: нижний уровень — уточнённая разметка исходных
// узлов, каждый следующий — уточнённая разметка свёрнутого графа. Это те
// самые уровни C3 → C0 из Microsoft GraphRAG («Advanced retrieval-augmented
// generation…», 2026, стр. 445), у которых описания верхних тем собираются
// из описаний нижних, а не из кусков заново.
//
// Всё детерминировано, как и Лувен: порядок узлов — по номеру, при равном
// приросте — меньший номер. В статье уточнение случайное (выбор
// с вероятностью по приросту); здесь берётся лучший прирост — иначе два
// запуска на одних данных дали бы разные темы, и сравнивать было бы нечего.

// LeidenResult — итог разбиения: нижняя разметка и лестница уровней.
type LeidenResult struct {
	// Bottom — узел → сообщество нижнего уровня (уточнённое, связное).
	Bottom map[uint32]uint32
	// Up[k] — сообщество уровня k → сообщество уровня k+1. Пусто, когда
	// нижний уровень и есть верхний (граф не свернулся ни разу).
	Up []map[uint32]uint32
	// Modularity — модулярность разметки верхнего уровня на исходном графе.
	Modularity float64
	// Rounds — сколько кругов перемещение/уточнение/свёртка сделано.
	Rounds int
}

// Levels — сколько уровней в иерархии (1 — только нижний).
func (r LeidenResult) Levels() int { return len(r.Up) + 1 }

// Top — узел → сообщество верхнего уровня.
func (r LeidenResult) Top() map[uint32]uint32 {
	out := make(map[uint32]uint32, len(r.Bottom))
	for id, c := range r.Bottom {
		for _, up := range r.Up {
			c = up[c]
		}
		out[id] = c
	}
	return out
}

// At — узел → сообщество уровня level (0 — нижний; выше верхнего — верхний).
func (r LeidenResult) At(level int) map[uint32]uint32 {
	out := make(map[uint32]uint32, len(r.Bottom))
	for id, c := range r.Bottom {
		for k := 0; k < level && k < len(r.Up); k++ {
			c = r.Up[k][c]
		}
		out[id] = c
	}
	return out
}

// leiden разбивает граф. resolution — тот же γ, что у Лувена.
func leiden(adj map[uint32]map[uint32]float64, order []uint32, resolution float64) LeidenResult {
	if resolution <= 0 {
		resolution = 1
	}
	res := LeidenResult{}
	// Текущий (свёрнутый) граф и разметка его узлов.
	g, nodes := adj, order
	comm := make(map[uint32]uint32, len(nodes))
	for _, id := range nodes {
		comm[id] = id
	}
	// Ни одного круга не сделано — нижний уровень пока одиночки.
	const maxRounds = 20
	for round := 0; round < maxRounds; round++ {
		moved := moveNodes(g, nodes, resolution, comm)
		refined := refine(g, nodes, resolution, comm)
		refined = renumber(refined, nodes)
		res.Rounds = round + 1
		distinct := map[uint32]bool{}
		for _, id := range nodes {
			distinct[refined[id]] = true
		}
		merged := len(distinct) < len(nodes)
		if round == 0 {
			res.Bottom = refined
		} else if merged {
			// Узлы свёрнутого графа — сообщества предыдущего уровня; уровень
			// без единого слияния — не уровень, а повтор предыдущего.
			res.Up = append(res.Up, refined)
		}
		// Не свернулось (каждый узел — своё уточнённое сообщество) или
		// перемещения ничего не сдвинули: верх достигнут.
		if !merged || !moved && round > 0 {
			break
		}
		// Свёртка по уточнённой разметке; разметка нового графа — из
		// разметки перемещений: уточнение только режет сообщества, поэтому
		// у всех узлов одного уточнённого сообщества comm одинаков.
		g, nodes = aggregate(g, nodes, refined)
		next := make(map[uint32]uint32, len(nodes))
		for old, r := range refined {
			if _, ok := next[r]; !ok {
				next[r] = comm[old]
			}
		}
		comm = next
	}
	if res.Bottom == nil {
		res.Bottom = renumber(comm, order)
	}
	res.Modularity = modularity(adj, order, res.Top(), resolution)
	return res
}

// refine собирает разметку заново внутри каждого сообщества comm: узлы
// начинают одиночками, и одиночка присоединяется к тому уточнённому
// сообществу своего же сообщества, с которым связана сильнее всего по
// приросту модулярности, — если прирост положителен. Узел без связей внутри
// сообщества остаётся один. Уже слитые узлы не двигаются (как в статье:
// «only nodes that are still in a singleton community are considered»).
func refine(adj map[uint32]map[uint32]float64, order []uint32, resolution float64, comm map[uint32]uint32) map[uint32]uint32 {
	degree := make(map[uint32]float64, len(order))
	var m2 float64
	for _, id := range order {
		for _, w := range adj[id] {
			degree[id] += w
			m2 += w
		}
	}
	out := make(map[uint32]uint32, len(order))
	for _, id := range order {
		out[id] = id
	}
	if m2 == 0 {
		return out
	}
	sumTot := make(map[uint32]float64, len(order))
	for _, id := range order {
		sumTot[id] = degree[id]
	}
	size := make(map[uint32]int, len(order)) // узлов в уточнённом сообществе
	for _, id := range order {
		size[id] = 1
	}
	for _, id := range order {
		if size[out[id]] != 1 {
			continue // уже не одиночка
		}
		toRef := map[uint32]float64{}
		for nb, w := range adj[id] {
			if nb == id || comm[nb] != comm[id] {
				continue
			}
			toRef[out[nb]] += w
		}
		if len(toRef) == 0 {
			continue
		}
		cands := make([]uint32, 0, len(toRef))
		for c := range toRef {
			cands = append(cands, c)
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i] < cands[j] })
		best, bestGain := id, 0.0
		for _, c := range cands {
			if c == id {
				continue
			}
			gain := toRef[c] - resolution*sumTot[c]*degree[id]/m2
			if gain > bestGain {
				best, bestGain = c, gain
			}
		}
		if best == id {
			continue
		}
		sumTot[id] -= degree[id]
		sumTot[best] += degree[id]
		size[id]--
		size[best]++
		out[id] = best
	}
	return out
}

// aggregate сворачивает узлы по разметке part в новый граф: узел — сообщество,
// вес между двумя — сумма весов связей их участников, внутренние связи —
// петля (сумма по обоим направлениям, чтобы степень узла равнялась сумме
// степеней участников, а общий вес графа не менялся).
func aggregate(adj map[uint32]map[uint32]float64, order []uint32,
	part map[uint32]uint32) (map[uint32]map[uint32]float64, []uint32) {

	out := map[uint32]map[uint32]float64{}
	for _, id := range order {
		a := part[id]
		if out[a] == nil {
			out[a] = map[uint32]float64{}
		}
		for nb, w := range adj[id] {
			out[a][part[nb]] += w
		}
	}
	ids := make([]uint32, 0, len(out))
	for id := range out {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return out, ids
}

// modularity — Q = Σ_c [ w_in(c)/m − γ·(sumTot(c)/2m)² ] по разметке comm
// исходного графа; w_in — внутренний вес сообщества (каждая связь один раз).
func modularity(adj map[uint32]map[uint32]float64, order []uint32, comm map[uint32]uint32, resolution float64) float64 {
	var m2 float64
	in := map[uint32]float64{}
	tot := map[uint32]float64{}
	for _, id := range order {
		c := comm[id]
		for nb, w := range adj[id] {
			m2 += w
			tot[c] += w
			if comm[nb] == c {
				in[c] += w
			}
		}
	}
	if m2 == 0 {
		return 0
	}
	q := 0.0
	for c, t := range tot {
		q += in[c]/m2 - resolution*(t/m2)*(t/m2)
	}
	return q
}

// disconnectedCommunities — сколько сообществ разметки не связны внутри себя
// (мера того, что Лейден обещает исправить, а Лувен допускает).
func disconnectedCommunities(adj map[uint32]map[uint32]float64, order []uint32, comm map[uint32]uint32) int {
	members := map[uint32][]uint32{}
	for _, id := range order {
		members[comm[id]] = append(members[comm[id]], id)
	}
	n := 0
	for _, list := range members {
		if len(list) < 2 {
			continue
		}
		if len(components(adj, list)) > 1 {
			n++
		}
	}
	return n
}
