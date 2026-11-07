package graph

// Встречные пары и круги в «является» и «часть» (этап 126, Я1).
//
// Оба типа по смыслу асимметричны: если A — частный случай B, то B не частный
// случай A; если A — часть B, то B не часть A. Пара A→B и B→A одного типа или
// круг A→B→C→A значит, что модель перепутала направление, назвала не тот тип
// или склейка свела в одно разные понятия. Книги называют это свойством
// отношений («relations in KGs exhibit diverse logical properties, such as
// transitivity, symmetry, and asymmetry», books/861#8), а круг ищется как
// компонента сильной связности (books/864#602).
//
// Первый замер 06.11.2026 на books (privatescripts/graphloops.py --asym):
// встречных пар «часть» 10567 (4,00 % направленных), «является» 2067 (1,73 %),
// в каждом типе по кругу-гиганту — 18965 и 5003 понятий. Счёт ничего не
// чинит: доктор показывает, решение — за человеком. Решение владельца по
// варианту (б) — правило перевеса в конце файла: прячет слабую сторону пары
// только в выдаче, файлы графа не трогает.
//
// Почему не вызов graphloops.py: доктор — прибор программы, а скрипт — второй
// путь по сырым файлам (edges.log и merges.jsonl), которым числа доктора
// сверяются. Счёт один и тот же: склейки применены, петли отброшены.

// Hierarchy — числа одного типа связи или обоих вместе.
type Hierarchy struct {
	Name     string // «является», «часть», «оба вместе»
	Records  int    // записей после склейки, без петель
	Pairs    int    // различных направленных пар
	Mutual   int    // пар понятий со связью в обе стороны (пара считается раз)
	Cycles   int    // компонент сильной связности больше одного понятия
	InCycles int    // понятий в них
	Largest  int    // крупнейшая такая компонента
}

// MutualShare — доля встречных пар от направленных, в процентах.
func (h Hierarchy) MutualShare() float64 {
	if h.Pairs == 0 {
		return 0
	}
	return 100 * float64(h.Mutual) / float64(h.Pairs)
}

// HierarchyCheck меряет «является», «часть» и оба типа вместе — так, как их
// видит выдача: с правилом перевеса (Rules.HierarchyMajority).
//
// Обход — по живым понятиям через Edges.Of, как у undirected: поглощённое
// понятие отдало связи выжившему, концы переведены к выжившим, петли после
// склейки отброшены.
func (g *Graph) HierarchyCheck() []Hierarchy { return g.hierarchyCheck(g.Edges().Of) }

// HierarchyCheckAsWritten — то же без правила перевеса: как записано в
// журнале связей (склейки наложены). Доктор печатает оба счёта рядом.
func (g *Graph) HierarchyCheckAsWritten() []Hierarchy {
	return g.hierarchyCheck(g.Edges().OfAsWritten)
}

func (g *Graph) hierarchyCheck(of func(uint32) []Edge) []Hierarchy {
	is, part := map[[2]uint32]int{}, map[[2]uint32]int{}
	for _, ent := range g.Entities().Live() {
		for _, ed := range of(ent.ID) {
			switch ed.Type {
			case RelIs:
				is[[2]uint32{ed.Src, ed.Dst}]++
			case RelPart:
				part[[2]uint32{ed.Src, ed.Dst}]++
			}
		}
	}
	both := make(map[[2]uint32]int, len(is)+len(part))
	for _, m := range []map[[2]uint32]int{is, part} {
		for p, n := range m {
			both[p] += n
		}
	}
	return []Hierarchy{
		hierarchyOf(RelName(RelIs), is),
		hierarchyOf(RelName(RelPart), part),
		hierarchyOf("оба вместе", both),
	}
}

// hierarchyOf считает числа по направленным парам с числом записей у каждой.
func hierarchyOf(name string, pairs map[[2]uint32]int) Hierarchy {
	h := Hierarchy{Name: name, Pairs: len(pairs)}
	adj := map[uint32][]uint32{}
	for p, n := range pairs {
		h.Records += n
		if p[0] < p[1] {
			if _, back := pairs[[2]uint32{p[1], p[0]}]; back {
				h.Mutual++
			}
		}
		adj[p[0]] = append(adj[p[0]], p[1])
	}
	for _, n := range sccSizes(adj) {
		if n < 2 {
			continue
		}
		h.Cycles++
		h.InCycles += n
		if n > h.Largest {
			h.Largest = n
		}
	}
	return h
}

// sccSizes — размеры компонент сильной связности (алгоритм Тарьяна без
// рекурсии: круг-гигант в десятки тысяч понятий переполнил бы стек вызовов
// глубиной обхода).
func sccSizes(adj map[uint32][]uint32) []int {
	index, low := map[uint32]int{}, map[uint32]int{}
	on := map[uint32]bool{}
	var stack []uint32
	var sizes []int
	next := 0
	type frame struct {
		v uint32
		i int
	}
	visit := func(v uint32) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		on[v] = true
	}
	for root := range adj {
		if _, seen := index[root]; seen {
			continue
		}
		visit(root)
		work := []frame{{v: root}}
		for len(work) > 0 {
			top := len(work) - 1
			v := work[top].v
			if work[top].i < len(adj[v]) {
				w := adj[v][work[top].i]
				work[top].i++
				if _, seen := index[w]; !seen {
					visit(w)
					work = append(work, frame{v: w})
				} else if on[w] && index[w] < low[v] {
					low[v] = index[w]
				}
				continue
			}
			work = work[:top]
			if top > 0 {
				if u := work[top-1].v; low[v] < low[u] {
					low[u] = low[v]
				}
			}
			if low[v] == index[v] {
				n := 0
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					on[w] = false
					n++
					if w == v {
						break
					}
				}
				sizes = append(sizes, n)
			}
		}
	}
	return sizes
}

// ── Правило перевеса (этап 126, Я1, вариант б) ──────────────────────────────
//
// У встречной пары одного типа («является» или «часть») слабая сторона при
// чтении не отдаётся, если у сильной записей не меньше чем в K раз больше.
// Записи — строки edges.log этой направленной тройки после склеек, петли не
// в счёт. Файлы графа не меняются: меняется только то, что отдают Of,
// incoming, Neighbors, Between и обходы поверх них.
//
// Замер `privatescripts/graphloops.py --asym --majority 3` (07.11.2026, books):
// убрано 4270 сторон (7447 записей), встречных «часть» 10567 → 7074,
// «является» 2067 → 1290; выборка 30 убранных глазами — верная сторона
// оставлена в 27. Слово владельца 07.11: K=3, «через файл конфига, если граф
// не меняется, а меняется лишь выдача».

// DefaultHierarchyMajority — K правила перевеса, когда в настройках ноль.
const DefaultHierarchyMajority = 3

// hierSide — направленная тройка «является»/«часть» после склеек.
type hierSide struct {
	src, dst uint32
	typ      uint8
}

// isHidden — скрыта ли связь правилом перевеса; концы уже переведены к выжившим.
func isHidden(hidden map[hierSide]int, ed Edge) bool {
	if len(hidden) == 0 || (ed.Type != RelIs && ed.Type != RelPart) {
		return false
	}
	_, ok := hidden[hierSide{ed.Src, ed.Dst, ed.Type}]
	return ok
}

// majoritySnap — скрытые стороны и то, по какому состоянию они посчитаны.
type majoritySnap struct {
	count  int              // записей связей при счёте (Edges.count)
	merges uint64           // поколение склеек при счёте (Merges.gen)
	hidden map[hierSide]int // скрытая сторона → её записей
}

// useMajority включает правило перевеса с этим K; ноль и меньше — выключено.
func (e *Edges) useMajority(k int) { e.majority = k }

// hiddenSides — стороны, которые правило перевеса прячет; nil — правило
// выключено или прятать нечего.
//
// Считается по всем записям один раз и пересчитывается, когда записей стало
// больше (Add во время сборки) или поменялись склейки: устаревший счёт
// прятал бы сторону, ставшую сильной. Пока ничего не менялось, читатели
// берут готовый набор без замка.
func (e *Edges) hiddenSides() map[hierSide]int {
	if e.majority <= 0 {
		return nil
	}
	fresh := func(s *majoritySnap, count int, gen uint64) bool {
		return s != nil && s.count == count && s.merges == gen
	}
	gen := e.merges.generation()
	e.mu.RLock()
	count := e.count
	e.mu.RUnlock()
	if s := e.snap.Load(); fresh(s, count, gen) {
		return s.hidden
	}
	e.hmu.Lock()
	defer e.hmu.Unlock()
	if s := e.snap.Load(); fresh(s, count, gen) {
		return s.hidden // посчитал другой читатель, пока этот ждал
	}
	s := e.countMajority(gen)
	e.snap.Store(s)
	return s.hidden
}

// countMajority считает скрытые стороны тем же правилом, что majority_filter
// в privatescripts/graphloops.py: при ab > 0 и ba > 0 слабая сторона —
// (b, a), если ab ≥ K·ba, иначе (a, b), если ba ≥ K·ab.
//
// Склейки поколения gen; если они поменялись по ходу счёта, отметка поколения
// окажется старой, и следующий читатель посчитает заново.
func (e *Edges) countMajority(gen uint64) *majoritySnap {
	n := map[hierSide]int{}
	e.mu.RLock()
	count := e.count
	for _, list := range e.bySrc {
		for _, ed := range list {
			if ed.Type != RelIs && ed.Type != RelPart {
				continue
			}
			a, b := e.merges.Resolve(ed.Src), e.merges.Resolve(ed.Dst)
			if a == b {
				continue // петля после склейки — её не видит и чтение
			}
			n[hierSide{a, b, ed.Type}]++
		}
	}
	e.mu.RUnlock()

	k := e.majority
	hidden := map[hierSide]int{}
	for s, ab := range n {
		if s.src > s.dst {
			continue // пара разбирается раз, со стороны меньшего номера
		}
		back := hierSide{s.dst, s.src, s.typ}
		ba := n[back]
		switch {
		case ba == 0:
		case ab >= k*ba:
			hidden[back] = ba
		case ba >= k*ab:
			hidden[s] = ab
		}
	}
	return &majoritySnap{count: count, merges: gen, hidden: hidden}
}

// MajorityHidden — сколько сторон встречных пар прячет правило перевеса и
// сколько в них записей. Нули — правило выключено или прятать нечего.
func (g *Graph) MajorityHidden() (sides, records int) {
	for _, n := range g.Edges().hiddenSides() {
		sides++
		records += n
	}
	return sides, records
}
