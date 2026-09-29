// hubmeasure — какой мерой считать «хаб» в цепочках (этап 105, Б5).
//
// **Откуда вопрос.** 25.09.2026 разбор файла замера Б5 показал: два обхода
// «как связаны X и Y» меряют степень узла по-разному. Обход по шагам
// спрашивает `len(Edges.Neighbors(id))`, а `Neighbors` ключуется парой
// {сосед, направление} — сосед, связанный в обе стороны, входит в список
// ДВАЖДЫ. Обход по потоку считал уникальных соседей. Расхождение починено
// (оба считают теперь парами), но остался вопрос, какая мера верна по сути:
// «хаб» — это понятие со множеством РАЗНЫХ соседей, а дубль по направлению
// выглядит свойством хранения, а не признаком хаба.
//
// **Почему нельзя просто поменять.** Порог 500 замерен 08.09.2026 (этап 101,
// D1) именно на мере `Neighbors`. Если та вдвое раздута, переход на уникальных
// соседей при том же числе 500 делает запрет примерно вдвое строже — это
// меняет рабочие цепочки, и менять на глаз нельзя.
//
// **Что считает этот замер.**
//  1. Раздувание меры по всем живым понятиям: во сколько раз `Neighbors`
//     больше числа уникальных соседей, и одинаково ли это по графу.
//  2. Сколько понятий считаются хабами при пороге по каждой мере.
//  3. Цену на деле: те же пары, тот же обход, два порога — что находится,
//     что теряется, как меняется длина и ширина середины.
//
// Карта не нужна: только чтение графа. Ничего не меняет.
package stats

import (
	"fmt"
	"sort"

	"github.com/BurntSushi/toml"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// hubDegrees — обе меры степени одного узла.
//
// pairs — как считает `Edges.Neighbors`: пары {сосед, направление}.
// uniq — сколько РАЗНЫХ понятий рядом.
type hubDegrees struct {
	pairs int
	uniq  int
}

// hubDegreeCache — степени узлов с запоминанием: в обходе один узел
// встречается десятки раз, а `Neighbors` каждый раз собирает карту заново.
type hubDegreeCache struct {
	g    *graph.Graph
	seen map[uint32]hubDegrees
}

func newHubDegreeCache(g *graph.Graph) *hubDegreeCache {
	return &hubDegreeCache{g: g, seen: map[uint32]hubDegrees{}}
}

func (c *hubDegreeCache) of(id uint32) hubDegrees {
	if d, ok := c.seen[id]; ok {
		return d
	}
	ns := c.g.Edges().Neighbors(id)
	uniq := map[uint32]bool{}
	for _, n := range ns {
		uniq[n.ID] = true
	}
	d := hubDegrees{pairs: len(ns), uniq: len(uniq)}
	c.seen[id] = d
	return d
}

// hubBFS — тот же обход, что в bfsAvoidingHubs, но мера степени задаётся
// снаружи. Иначе замер сравнивал бы не две меры, а два разных обхода.
//
// Возвращает длину цепочки (0 — не найдена) и наибольшую степень середины —
// **всегда по уникальным соседям**, какой бы мерой ни шёл запрет. Первый
// прогон 25.09.2026 печатал ширину той же мерой, какой запрещал, и «середина
// 490 → 389» читалось как «путь сузился», хотя путь был тот же самый, просто
// померенный двумя линейками. Прибор обязан мерить одним, меняя только то,
// что проверяется (правило «прибор — часть замера»).
func hubBFS(c *hubDegreeCache, from, to uint32, maxHops, limit int, byUniq bool) (int, int) {
	deg := func(id uint32) int {
		d := c.of(id)
		if byUniq {
			return d.uniq
		}
		return d.pairs
	}
	width := func(id uint32) int { return c.of(id).uniq }
	type node struct {
		id     uint32
		dist   int
		widest int
	}
	seen := map[uint32]bool{from: true}
	queue := []node{{from, 0, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.dist >= maxHops {
			continue
		}
		for _, n := range c.g.Edges().Neighbors(cur.id) {
			if seen[n.ID] {
				continue
			}
			if n.ID == to {
				return cur.dist + 1, cur.widest
			}
			if limit > 0 && deg(n.ID) >= limit {
				continue // через хаб не ходим
			}
			seen[n.ID] = true
			w := cur.widest
			if d := width(n.ID); d > w {
				w = d
			}
			queue = append(queue, node{n.ID, cur.dist + 1, w})
		}
	}
	return 0, 0
}

// hubMeasure — сам замер. pairsPath — набор пар понятий (тот же, что у -chains).
func hubMeasure(g *graph.Graph, pairsPath string, hops, limit int) {
	if limit <= 0 {
		limit = g.Rules().ChainHubLimit
	}
	if limit <= 0 {
		limit = graph.DefaultChainHubLimit
	}
	c := newHubDegreeCache(g)

	fmt.Printf("\nМера «хаба»: пары {сосед, направление} против уникальных соседей\n")
	fmt.Printf("Порог: %d\n", limit)

	// 1. Раздувание по всему графу.
	live := g.Entities().Live()
	var ratios []float64
	hubsByPairs, hubsByUniq, both := 0, 0, 0
	sumPairs, sumUniq, withEdges := 0, 0, 0
	for _, ent := range live {
		d := c.of(ent.ID)
		if d.uniq == 0 {
			continue
		}
		withEdges++
		sumPairs += d.pairs
		sumUniq += d.uniq
		ratios = append(ratios, float64(d.pairs)/float64(d.uniq))
		hp, hu := d.pairs >= limit, d.uniq >= limit
		if hp {
			hubsByPairs++
		}
		if hu {
			hubsByUniq++
		}
		if hp && hu {
			both++
		}
	}
	sort.Float64s(ratios)
	med := func(v []float64) float64 {
		if len(v) == 0 {
			return 0
		}
		return v[len(v)/2]
	}
	fmt.Printf("\n1. Раздувание меры (живых понятий со связями: %d)\n", withEdges)
	fmt.Printf("   медиана Neighbors/уникальных: %.3f, среднее по суммам: %.3f\n",
		med(ratios), avg(sumPairs, sumUniq))
	// Распределение: если раздувание всюду одинаково, порог можно просто
	// поделить; если нет — у мер разный смысл, и делением не обойтись.
	buckets := []struct {
		name string
		lo   float64
		hi   float64
		n    int
	}{
		{"ровно 1 (связи только в одну сторону)", 0, 1.0001, 0},
		{"1—1,25", 1.0001, 1.25, 0},
		{"1,25—1,5", 1.25, 1.5, 0},
		{"1,5—1,75", 1.5, 1.75, 0},
		{"1,75—2", 1.75, 2.0001, 0},
	}
	for _, r := range ratios {
		for i := range buckets {
			if r > buckets[i].lo && r <= buckets[i].hi {
				buckets[i].n++
				break
			}
		}
	}
	for _, b := range buckets {
		fmt.Printf("   %-40s %6d (%4.1f%%)\n", b.name, b.n, pct(b.n, withEdges))
	}

	fmt.Printf("\n2. Кого порог %d считает хабом\n", limit)
	fmt.Printf("   по парам {сосед, направление}: %d понятий\n", hubsByPairs)
	fmt.Printf("   по уникальным соседям:         %d понятий\n", hubsByUniq)
	fmt.Printf("   и там и там:                   %d; перестали бы быть хабами: %d\n",
		both, hubsByPairs-both)

	// 3. Цена на наборе пар.
	var set struct {
		Case []struct {
			ConceptA string `toml:"concept_a"`
			ConceptB string `toml:"concept_b"`
		} `toml:"case"`
	}
	if _, err := toml.DecodeFile(pairsPath, &set); err != nil {
		die(err)
	}
	type res struct{ found, missing, lenSum, wideSum, widest int }
	var byPairs, byUniq res
	lostByUniq, gainedByUniq, changedPath := 0, 0, 0
	var lostExamples, narrowedExamples []string
	pairsTotal := 0
	for _, cs := range set.Case {
		a, okA := g.Entities().Lookup(cs.ConceptA)
		b, okB := g.Entities().Lookup(cs.ConceptB)
		if !okA || !okB {
			continue
		}
		if len(g.Edges().Between(a.ID, b.ID)) > 0 || len(g.Edges().Between(b.ID, a.ID)) > 0 {
			continue // прямая связь — цепочка не нужна
		}
		pairsTotal++
		nP, wP := hubBFS(c, a.ID, b.ID, hops, limit, false)
		nU, wU := hubBFS(c, a.ID, b.ID, hops, limit, true)
		acc := func(r *res, n, w int) {
			if n > 0 {
				r.found++
				r.lenSum += n
				r.wideSum += w
				if w > r.widest {
					r.widest = w
				}
			} else {
				r.missing++
			}
		}
		acc(&byPairs, nP, wP)
		acc(&byUniq, nU, wU)
		switch {
		case nP > 0 && nU == 0:
			lostByUniq++
			if len(lostExamples) < 8 {
				lostExamples = append(lostExamples,
					fmt.Sprintf("%s ↔ %s (была %d шага, середина до %d)",
						cut(cs.ConceptA, 26), cut(cs.ConceptB, 26), nP, wP))
			}
		case nP == 0 && nU > 0:
			gainedByUniq++
		case nP > 0 && nU > 0 && (wU != wP || nU != nP):
			// Обе ширины — по уникальным соседям, так что разница значит
			// РАЗНЫЙ ПУТЬ, а не разную линейку.
			changedPath++
			if len(narrowedExamples) < 8 {
				narrowedExamples = append(narrowedExamples,
					fmt.Sprintf("%s ↔ %s (середина %d → %d, длина %d → %d)",
						cut(cs.ConceptA, 26), cut(cs.ConceptB, 26), wP, wU, nP, nU))
			}
		}
	}

	fmt.Printf("\n3. Цепочки на наборе %s (пар без прямой связи: %d, предел %d шага)\n",
		pairsPath, pairsTotal, hops)
	fmt.Printf("   (ширина середины всюду по УНИКАЛЬНЫМ соседям — одна линейка\n")
	fmt.Printf("    для обоих порогов, иначе сравнивались бы не пути, а меры)\n")
	row := func(name string, r res) {
		fmt.Printf("   %-28s найдено %2d, нет %2d, средняя длина %.2f, средняя ширина середины %.0f, худшая %d\n",
			name, r.found, r.missing, avg(r.lenSum, r.found), avg(r.wideSum, r.found), r.widest)
	}
	row("порог по парам", byPairs)
	row("порог по уникальным", byUniq)
	fmt.Printf("   ЦЕНА перехода на уникальных: цепочек пропало %d, появилось %d, путь изменился %d\n",
		lostByUniq, gainedByUniq, changedPath)
	for _, s := range lostExamples {
		fmt.Printf("     пропала · %s\n", s)
	}
	for _, s := range narrowedExamples {
		fmt.Printf("     путь другой · %s\n", s)
	}
}
