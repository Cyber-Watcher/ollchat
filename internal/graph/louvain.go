package graph

import "sort"

// Louvain: жадное разбиение графа на сообщества по модулярности.
//
// Устройство простое. Сначала каждый узел — своё сообщество. Затем узлы
// по очереди перекладываются в то соседнее сообщество, где прирост
// модулярности наибольший; проход повторяется, пока хоть что-то двигается.
//
// Модулярность — мера того, насколько связей внутри групп больше, чем было бы
// в случайном графе с теми же степенями узлов. Прирост от переноса узла i
// в сообщество C считается как
//
//	dQ = k_i_in - γ * sumTot(C) * k_i / (2m)
//
// где k_i_in — вес связей узла i с этим сообществом, sumTot(C) — суммарная
// степень сообщества, k_i — степень узла, m — вес всех связей. Постоянные
// множители опущены: сравниваются приросты между собой, а не с нулём.
//
// **Все обходы детерминированы.** Порядок узлов — по возрастанию номера,
// при равном приросте берётся сообщество с меньшим номером. Без этого
// два запуска на одних данных дают разные сообщества, и объяснить разницу
// нечем — см. пояснение в community.go.

// louvain возвращает разметку «узел → номер сообщества».
//
// resolution — множитель при штрафе за размер сообщества (в литературе γ).
// Единица — обычная модулярность. Больше единицы — сообщества выходят мельче
// **сразу**, а не дроблением задним числом: штраф за присоединение к крупному
// сообществу растёт, и узел охотнее остаётся в своём.
//
// Рычаг понадобился там, где дробление бессильно. Замер 26.08.2026 на графе
// из 62 805 понятий: после шестого прохода дробления цифры перестают меняться
// вовсе, а семь сообществ остаются крупнее предела — Louvain отказывается их
// делить, потому что внутри они связаны одинаково плотно. Причина — узлы-
// концентраторы: `LLM` и `ChatGPT` связаны со всем подряд, и вокруг них
// слипается всё, что упоминалось рядом. Дроблению это не поддаётся,
// разрешению — поддаётся.
func louvain(adj map[uint32]map[uint32]float64, order []uint32, resolution float64) map[uint32]uint32 {
	if resolution <= 0 {
		resolution = 1
	}
	comm := make(map[uint32]uint32, len(order))
	degree := make(map[uint32]float64, len(order))
	var m2 float64 // удвоенный вес всех связей
	for _, id := range order {
		comm[id] = id
		for _, w := range adj[id] {
			degree[id] += w
			m2 += w
		}
	}
	if m2 == 0 {
		return comm
	}

	sumTot := make(map[uint32]float64, len(order))
	for _, id := range order {
		sumTot[id] = degree[id]
	}

	// Проходов немного: разбиение устаканивается за единицы итераций,
	// а предел спасает от бесконечного качания на симметричных графах.
	const maxPasses = 20
	for pass := 0; pass < maxPasses; pass++ {
		moved := false
		for _, id := range order {
			cur := comm[id]
			sumTot[cur] -= degree[id]

			// Вес связей узла с каждым соседним сообществом.
			toComm := map[uint32]float64{}
			for nb, w := range adj[id] {
				toComm[comm[nb]] += w
			}

			best, bestGain := cur, toComm[cur]-resolution*sumTot[cur]*degree[id]/m2
			cands := make([]uint32, 0, len(toComm))
			for c := range toComm {
				cands = append(cands, c)
			}
			sort.Slice(cands, func(i, j int) bool { return cands[i] < cands[j] })
			for _, c := range cands {
				gain := toComm[c] - resolution*sumTot[c]*degree[id]/m2
				// Строгое «больше» плюс отсортированный обход и означают
				// «при равенстве — меньший номер».
				if gain > bestGain {
					best, bestGain = c, gain
				}
			}

			sumTot[best] += degree[id]
			if best != cur {
				comm[id] = best
				moved = true
			}
		}
		if !moved {
			break
		}
	}
	return renumber(comm, order)
}

// renumber перенумеровывает сообщества подряд, в порядке первого появления
// при обходе узлов по возрастанию. Иначе номера сообществ — это номера
// случайных узлов, и разбиение нельзя сравнить с прошлым запуском.
func renumber(comm map[uint32]uint32, order []uint32) map[uint32]uint32 {
	seen := map[uint32]uint32{}
	out := make(map[uint32]uint32, len(comm))
	var next uint32
	for _, id := range order {
		c := comm[id]
		n, ok := seen[c]
		if !ok {
			n = next
			seen[c] = n
			next++
		}
		out[id] = n
	}
	return out
}

// rollUp сворачивает сообщества в узлы: вес связи между двумя сообществами —
// сумма весов связей между их участниками. На этом свёрнутом графе ищется
// разбиение второго уровня.
func rollUp(adj map[uint32]map[uint32]float64, order []uint32,
	comm map[uint32]uint32) (map[uint32]map[uint32]float64, []uint32) {

	out := map[uint32]map[uint32]float64{}
	add := func(a, b uint32, w float64) {
		if out[a] == nil {
			out[a] = map[uint32]float64{}
		}
		out[a][b] += w
	}
	for _, id := range order {
		a := comm[id]
		for nb, w := range adj[id] {
			b := comm[nb]
			if a == b {
				continue // связи внутри сообщества на верхнем уровне не нужны
			}
			add(a, b, w)
		}
	}
	ids := make([]uint32, 0, len(out))
	for id := range out {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return out, ids
}

// defaultSplitDepth — сколько раз подряд дробить крупные сообщества.
//
// Шесть, а не три и не двадцать: замер 26.08.2026 на графе из 62 805 понятий
// показал, что после шестого прохода цифры перестают меняться **вовсе** —
// совпадают до единицы на глубинах 6, 8, 10, 15 и 20. Дробить дальше нечего.
const defaultSplitDepth = 6

// DefaultResolution — разрешение по умолчанию (γ).
//
// Пятёрка выбрана по замеру 26.08.2026, и признак был числовой, а не на глаз:
// число сообществ размером от пяти понятий — то есть тех, что вообще годятся
// в тему, — растёт до γ = 5 и падает после. При γ = 1 семь сообществ остаются
// крупнее предела и дроблением не берутся; при γ = 12 и выше настоящие темы
// начинают крошиться в мелочь ниже порога.
//
// Проверка глазами подтвердила числа: при γ = 1 крупнейшее сообщество —
// каша из «LLM, ChatGPT, Notion, Trello, Яндекс Таблицы, Samsung»,
// при γ = 5 четыре крупнейших читаются как темы: тензоры, Mixture of experts,
// эмбеддинги, оценка OCR и VLM.
//
// Число измерено на **этом** графе. На заметно меньшем оно может оказаться
// великоватым — потому и вынесено в CommunityOpts, а не зашито.
const DefaultResolution = 5.0

// splitLarge дробит слишком крупные сообщества, запуская разбиение заново
// на их же подграфе.
//
// Зачем это понадобилось. Первый прогон на настоящем графе (38 664 понятия,
// 115 207 связей) дал сообщество на 3 662 понятия при медиане в 3. Так ведёт
// себя Louvain на графе с крупными узлами-концентраторами: вокруг «LLM»
// и «Claude Code» слипается всё подряд. Ком из трёх с половиной тысяч понятий
// — это не тема, и резюме по нему написать нельзя: модель получит список,
// в котором нет общего смысла.
//
// Дробление рекурсивное, но неглубокое: подграф каждый раз меньше, и трёх
// уровней хватает, чтобы уложиться в предел. Если сообщество не дробится
// (все понятия связаны одинаково плотно), оно остаётся как есть — насильно
// резать связное на части хуже, чем оставить крупным.
func splitLarge(adj map[uint32]map[uint32]float64, order []uint32,
	comm map[uint32]uint32, maxSize, maxDepth int, resolution float64) map[uint32]uint32 {

	if maxSize <= 0 || maxDepth <= 0 {
		return comm
	}
	out := make(map[uint32]uint32, len(comm))
	for k, v := range comm {
		out[k] = v
	}
	next := uint32(0)
	for _, c := range out {
		if c >= next {
			next = c + 1
		}
	}

	for depth := 0; depth < maxDepth; depth++ {
		members := map[uint32][]uint32{}
		for _, id := range order {
			members[out[id]] = append(members[out[id]], id)
		}
		big := make([]uint32, 0)
		for c, list := range members {
			if len(list) > maxSize {
				big = append(big, c)
			}
		}
		if len(big) == 0 {
			break
		}
		sort.Slice(big, func(i, j int) bool { return big[i] < big[j] })

		for _, c := range big {
			list := members[c]
			sub := induced(adj, list)
			parts := louvain(sub, list, resolution)
			// Разбиения не вышло: все в одном куске — оставляем как есть.
			distinct := map[uint32]bool{}
			for _, id := range list {
				distinct[parts[id]] = true
			}
			if len(distinct) < 2 {
				continue
			}
			// Первый кусок наследует прежний номер, остальные получают новые:
			// так номера не переиспользуются и разбиение остаётся сравнимым.
			renamed := map[uint32]uint32{}
			for _, id := range list {
				p := parts[id]
				n, ok := renamed[p]
				if !ok {
					if len(renamed) == 0 {
						n = c
					} else {
						n = next
						next++
					}
					renamed[p] = n
				}
				out[id] = n
			}
		}
	}
	return renumber(out, order)
}

// induced строит подграф на заданных узлах: связи наружу отбрасываются,
// потому что дробим мы именно внутреннее устройство сообщества.
func induced(adj map[uint32]map[uint32]float64, list []uint32) map[uint32]map[uint32]float64 {
	in := make(map[uint32]bool, len(list))
	for _, id := range list {
		in[id] = true
	}
	out := make(map[uint32]map[uint32]float64, len(list))
	for _, id := range list {
		row := map[uint32]float64{}
		for nb, w := range adj[id] {
			if in[nb] {
				row[nb] = w
			}
		}
		out[id] = row
	}
	return out
}
