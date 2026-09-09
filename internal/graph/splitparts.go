package graph

import "sort"

// Разрез несвязных тем на связные части (этап 101, Г1).
//
// **Откуда взялось.** «Advanced retrieval-augmented generation» (2026, стр. 245):
// «Leiden ... ensures well-connected communities and avoids disconnected subgroups»
// — «Leiden гарантирует связные сообщества и не допускает несвязных подгрупп».
// Первоисточник назван там же: Traag, Waltman, Van Eck, «From Louvain to Leiden:
// Guaranteeing well-connected communities», Scientific Reports 9:5233, 2019.
//
// Louvain такой гарантии не даёт: тема может состоять из двух половин без единой
// связи между ними, и описание такой темы моделью — описание двух разных вещей
// под одним заголовком.
//
// **Почему не Leiden.** Замер 08.09.2026: несвязных тем 41 из 47 466 (0.09%).
// Переписывать разбиение ради 0.09% — недели работы и потеря воспроизводимости,
// ради которой Louvain у нас и обходит узлы по возрастанию номера. Разрез
// постфактум даёт тот же итог для нашего случая: после него несвязных тем нет
// вовсе, а стоит он одного обхода уже построенной матрицы смежности.
//
// **Что достаётся частям.** Крупнейшая часть остаётся прежней темой и сохраняет
// её номер и описание: состав изменился мало, и описание всё ещё про неё.
// Остальные части становятся новыми темами без описания — их напишет ближайший
// `--graph-summaries`, и это единственная цена разреза в работе карты.
//
// Уровень 1 не режется намеренно: тема верхнего уровня — объединение мелких,
// её несвязность не ошибка, а устройство.

// splitDisconnected разрезает несвязные темы нижнего уровня на связные части.
// Второе значение — сколько тем оказалось разрезано.
func splitDisconnected(adj map[uint32]map[uint32]float64, list []Community) ([]Community, int) {
	var out []Community
	var split int
	for _, com := range list {
		if com.Level != 0 || len(com.Members) < 2 {
			out = append(out, com)
			continue
		}
		parts := components(adj, com.Members)
		if len(parts) < 2 {
			out = append(out, com)
			continue
		}
		split++
		out = append(out, cutInto(adj, com, parts)...)
	}
	return out, split
}

// cutInto превращает одну несвязную тему в несколько связных.
func cutInto(adj map[uint32]map[uint32]float64, com Community, parts [][]uint32) []Community {
	// Крупнейшая часть идёт первой: ей достаются номер и описание темы.
	// При равном размере верх берёт часть с меньшим номером первого понятия —
	// иначе два запуска на одних данных дали бы разные номера тем.
	sort.SliceStable(parts, func(i, j int) bool {
		if len(parts[i]) != len(parts[j]) {
			return len(parts[i]) > len(parts[j])
		}
		return minID(parts[i]) < minID(parts[j])
	})

	out := make([]Community, 0, len(parts))
	for i, part := range parts {
		// Порядок участников берётся из исходной темы: он не случаен —
		// первыми идут самые упоминаемые, и по ним модель называет тему.
		keep := make(map[uint32]bool, len(part))
		for _, id := range part {
			keep[id] = true
		}
		members := make([]uint32, 0, len(part))
		for _, id := range com.Members {
			if keep[id] {
				members = append(members, id)
			}
		}

		next := Community{
			ID:      com.ID,
			Level:   com.Level,
			Parent:  com.Parent,
			Members: members,
			Weight:  inner(adj, members),
		}
		if i == 0 {
			// Описание остаётся у крупнейшей части вместе с оценкой и книгами.
			next.Title, next.Summary, next.Key = com.Title, com.Summary, com.Key
			next.Books, next.Rating, next.Why = com.Books, com.Rating, com.Why
		} else {
			// Номер новой темы — наименьший номер понятия в ней. Темы не
			// пересекаются по участникам, поэтому такой номер не может совпасть
			// с номером другой темы.
			next.ID = int(minID(part))
		}
		out = append(out, next)
	}
	return out
}

func minID(part []uint32) uint32 {
	m := part[0]
	for _, id := range part[1:] {
		if id < m {
			m = id
		}
	}
	return m
}
