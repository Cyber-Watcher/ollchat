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

// TuneRow — что вышло при одном наборе пределов.
type TuneRow struct {
	Resolution float64

	// Beta — сколько смысла подмешано в веса связей на этой строке.
	// Ноль — связи как есть.
	Beta float64

	// Blend — что дало подмешивание: измеренный фон, сколько рёбер тронуто,
	// у скольких не нашлось вектора. Пусто при Beta = 0.
	Blend      SenseBlend
	Topics     int     // сообществ уровня 0
	Largest    int     // размер самого крупного
	Median     int     // размер срединного
	Oversized  int     // сколько крупнее предела MaxSize
	Singletons int     // сколько из одного понятия: дробление ушло в песок
	Cohesion   float64 // медианная доля связей, не выходящих за пределы темы

	// Cos — смысловая связность тем по векторам понятий: насколько понятия
	// темы ближе друг к другу, чем к случайному понятию графа. Связность выше
	// (Cohesion) считает то же самое по связям, и обе меры нужны порознь:
	// разбиение оптимизирует именно связи, поэтому по ним оно всегда выглядит
	// хорошо, а векторы в счёт не входили и потому судят со стороны.
	//
	// Пусто, если векторы понятий не посчитаны, — см. ThemeCosine.Ready.
	Cos ThemeCosine

	// CosNull — та же мера на разбиении с перемешанным составом: темы тех же
	// размеров, набранные случайно. Нулевая гипотеза, без которой Cos нечитаем:
	// зазор 0.058 — это много или мало, видно только рядом со случайным.
	CosNull ThemeCosine

	Samples []TuneSample
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
		row, err := g.tuneOne(opt, samples, names)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// TuneBeta перебирает β — долю смысла в весе связи — при неизменном γ.
//
// Отдельным перебором, а не столбцом в общем: γ и β меняют разное, и таблица,
// где обе крутятся разом, не отвечает ни на один вопрос. Замер 06.09.2026
// показал, что γ до рыхлости крупных тем не достаёт; β — попытка достать
// до неё другим сигналом, и мерить её надо при зафиксированном γ.
func (g *Graph) TuneBeta(base CommunityOpts, betas []float64, samples, names int) ([]TuneRow, error) {
	if samples <= 0 {
		samples = 3
	}
	if names <= 0 {
		names = 6
	}
	out := make([]TuneRow, 0, len(betas))
	for _, b := range betas {
		opt := base
		opt.Beta = b
		row, err := g.tuneOne(opt, samples, names)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// tuneOne — одно разбиение и все числа по нему.
func (g *Graph) tuneOne(opt CommunityOpts, samples, names int) (TuneRow, error) {
	res, err := g.PartitionOnly(opt)
	if err != nil {
		return TuneRow{}, err
	}
	row := TuneRow{Resolution: opt.norm().Resolution, Beta: opt.Beta, Blend: res.Blend}

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

	// Смысловая связность — со стороны векторов, которых разбиение
	// не видело. Векторов нет — поле остаётся пустым, подбор идёт как прежде.
	row.Cos = g.ThemeCosineOf(res, 0)
	row.CosNull = g.ThemeCosineOf(ShuffledLevel0(res), 0)

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
	return row, nil
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
