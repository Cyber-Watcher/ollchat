package graph

import "sort"

// Подбор разрешения разбиения.
//
// **Зачем.** Разрешение γ у Лувена решает, насколько мелко режется граф, и это
// единственная ручка, которая по-настоящему меняет форму карты тем. Подбирать
// её на глаз нельзя, а мерить дорого-то и не надо: разбиение считается
// на процессоре за секунды, модель не нужна вовсе.
//
// **Почему числа без состава — недостаточно.** Замер 26.08.2026: разбиение
// с одним значением γ дало «тему» из 3 662 понятий, и по числам она выглядела
// как обычная крупная тема. Заглянув в состав, стало видно, что там свалено
// всё подряд. Числа не отличают «тема из сорока понятий про RAG» от «тема
// из сорока понятий обо всём сразу», а глаз отличает мгновенно, — поэтому
// вместе с числами показываются имена.
//
// Глубина дробления (MaxDepth) на форму почти не влияет: замер того же дня
// показал, что она сходится к шести и крупные слипшиеся темы не разбивает.
// Настоящая ручка — γ.

// TuneRow — что вышло при одном значении разрешения.
type TuneRow struct {
	Resolution float64
	Topics     int     // сообществ уровня 0
	Largest    int     // размер самого крупного
	Median     int     // размер срединного
	Oversized  int     // сколько крупнее предела MaxSize
	Singletons int     // сколько из одного понятия: дробление ушло в песок
	Cohesion   float64 // медианная доля связей, не выходящих за пределы темы
	Samples    []TuneSample
}

// TuneSample — одна тема составом: числа врут реже, когда рядом имена.
type TuneSample struct {
	Members int
	Names   []string
}

// Tune строит разбиение для каждого значения γ и сравнивает.
//
// Ничего не сохраняет: подбор обязан быть безобидным, иначе им не будут
// пользоваться из осторожности.
func (g *Graph) Tune(base CommunityOpts, resolutions []float64, samples, names int) ([]TuneRow, error) {
	if samples <= 0 {
		samples = 3
	}
	if names <= 0 {
		names = 6
	}
	out := make([]TuneRow, 0, len(resolutions))
	for _, r := range resolutions {
		opt := base
		opt.Resolution = r
		res, err := g.PartitionOnly(opt)
		if err != nil {
			return nil, err
		}
		row := TuneRow{Resolution: r}

		lvl0 := res.Level(0)
		sizes := make([]int, 0, len(lvl0))
		for _, c := range lvl0 {
			sizes = append(sizes, len(c.Members))
			if len(c.Members) == 1 {
				row.Singletons++
			}
			if opt.norm().MaxSize > 0 && len(c.Members) > opt.norm().MaxSize {
				row.Oversized++
			}
		}
		row.Topics = len(lvl0)
		if len(sizes) > 0 {
			sort.Ints(sizes)
			row.Largest = sizes[len(sizes)-1]
			row.Median = sizes[len(sizes)/2]
		}

		// Связность считаем по тем же правилам, что и сито --graph-recheck:
		// доля связей, не выходящих за пределы темы.
		row.Cohesion = medianCohesion(g, res)

		sort.Slice(lvl0, func(i, j int) bool { return len(lvl0[i].Members) > len(lvl0[j].Members) })
		for i, c := range lvl0 {
			if i >= samples {
				break
			}
			s := TuneSample{Members: len(c.Members)}
			for _, m := range c.Members {
				if len(s.Names) >= names {
					break
				}
				if e, ok := g.Entities().Get(m); ok {
					s.Names = append(s.Names, e.Name)
				}
			}
			row.Samples = append(row.Samples, s)
		}
		out = append(out, row)
	}
	return out, nil
}

// medianCohesion — срединная связность тем крупнее двадцати понятий.
//
// Мелкие темы в счёт не идут: у темы из трёх понятий доля связей ничего
// не говорит, а таких тем большинство, и они утянули бы медиану к нулю.
func medianCohesion(g *Graph, c *Communities) float64 {
	owner := make(map[uint32]int)
	for _, com := range c.List {
		if com.Level != 0 {
			continue
		}
		for _, m := range com.Members {
			owner[m] = com.ID
		}
	}
	inside, outside := map[int]int{}, map[int]int{}
	for member, id := range owner {
		for _, ed := range g.Edges().Of(member) {
			if owner[ed.Dst] == id {
				inside[id]++
			} else {
				outside[id]++
			}
		}
	}
	var shares []float64
	for _, com := range c.List {
		if com.Level != 0 || len(com.Members) < 20 {
			continue
		}
		in, ex := inside[com.ID], outside[com.ID]
		if in+ex == 0 {
			continue
		}
		shares = append(shares, float64(in)/float64(in+ex))
	}
	if len(shares) == 0 {
		return 0
	}
	sort.Float64s(shares)
	return shares[len(shares)/2]
}
