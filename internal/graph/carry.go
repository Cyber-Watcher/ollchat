package graph

import "sort"

// Перенос описаний при пересчёте разбиения.
//
// **Зачем.** Разбиение считается за секунды, а всё, что после него, — часами:
// названия, резюме, оценки и разборы к новому разбиению не переносились вовсе,
// и каждый пересчёт стоил полутора часов работы видеокарты (35 минут резюме,
// 22 разборы, 20 векторы понятий). Из-за этой цены пересчёт откладывали,
// и карта тем отставала от графа: свежие каталоги не образовывали своих тем.
//
// **Почему это вообще возможно.** Замер 27.08.2026 (`--graph-drift`): при
// пересчёте на выросшем графе **89% тем сохранили бы почти прежний состав**.
// То есть девять описаний из десяти писались бы заново без всякой нужды.
//
// **Как сопоставляются темы.** По доле общих понятий (Жаккар): пересечение
// делить на объединение. Модель для этого не нужна — это арифметика на списках.
// Порог по умолчанию 0.7, и он **настраиваемый**, потому что подбирается
// замером, а не назначается: слишком низкий перенесёт описание на тему, которая
// уже про другое, слишком высокий не перенесёт ничего.
//
// **Что переносится и что нет.** Переносится всё, что писала модель: название,
// резюме, ключевые понятия, оценка, объяснение оценки, разбор. Не переносится
// состав — он у новой темы свой. Перенесённое помечается, чтобы человек видел:
// это описание писалось не по этому в точности набору понятий.

// carryThreshold — с какой доли общих понятий описание считается годным
// для переноса.
const carryThreshold = 0.7

// CarryResult — что дал перенос.
type CarryResult struct {
	Carried int // тем, получивших прежнее описание

	// CarriedFindings — из них с готовым разбором. Считается отдельно, потому
	// что разбор дороже резюме и есть далеко не у всех тем: смешивать их
	// в одну оценку времени значит выдумывать число.
	CarriedFindings int
	Fresh           int // новых тем без описания
	Lost            int // прежних описанных тем, которым не нашлось преемника
	Total           int // тем нулевого уровня в новом разбиении
}

// carryDescriptions переносит описания со старого разбиения на новое.
//
// Порядок такой: для каждой НОВОЙ темы ищется старая с наибольшим пересечением.
// Именно в эту сторону, а не наоборот: одна старая тема могла распасться
// на две новых, и обе имеют право на её описание — но только если каждая
// достаточно на неё похожа.
//
// Одно старое описание переносится не больше одного раза: если тема распалась,
// описание достанется той половине, что похожа сильнее. Иначе две разные темы
// получили бы одинаковое название, и обзор стал бы врать.
func carryDescriptions(old, fresh *Communities, threshold float64) CarryResult {
	var res CarryResult
	if old == nil || fresh == nil {
		return res
	}
	if threshold <= 0 {
		threshold = carryThreshold
	}

	// Понятие → старая тема. Только нулевой уровень: у объединений в Members
	// лежат номера тем, а не понятий.
	owner := make(map[uint32]int)
	sizeOld := make(map[int]int)
	byID := make(map[int]*Community)
	for i := range old.List {
		com := &old.List[i]
		if com.Level != 0 || com.Title == "" {
			continue
		}
		byID[com.ID] = com
		sizeOld[com.ID] = len(com.Members)
		for _, m := range com.Members {
			owner[m] = com.ID
		}
	}

	// Кандидаты на перенос: для каждой новой темы — лучшая старая и мера
	// сходства. Собираем все, потом раздаём по убыванию сходства, чтобы
	// при споре описание досталось более похожей.
	type claim struct {
		freshIdx int
		oldID    int
		sim      float64
	}
	var claims []claim
	for i := range fresh.List {
		com := &fresh.List[i]
		if com.Level != 0 {
			continue
		}
		res.Total++
		hits := map[int]int{}
		for _, m := range com.Members {
			if id, ok := owner[m]; ok {
				hits[id]++
			}
		}
		best, bestID := 0, 0
		for id, n := range hits {
			if n > best {
				best, bestID = n, id
			}
		}
		if best == 0 {
			continue
		}
		union := len(com.Members) + sizeOld[bestID] - best
		if union <= 0 {
			continue
		}
		if sim := float64(best) / float64(union); sim >= threshold {
			claims = append(claims, claim{freshIdx: i, oldID: bestID, sim: sim})
		}
	}

	sort.Slice(claims, func(a, b int) bool {
		if claims[a].sim != claims[b].sim {
			return claims[a].sim > claims[b].sim
		}
		return claims[a].freshIdx < claims[b].freshIdx // ради устойчивости
	})

	taken := make(map[int]bool, len(claims))
	for _, c := range claims {
		if taken[c.oldID] {
			continue // описание уже ушло более похожей теме
		}
		src := byID[c.oldID]
		dst := &fresh.List[c.freshIdx]
		dst.Title = src.Title
		dst.Summary = src.Summary
		dst.Key = src.Key
		dst.Books = src.Books
		dst.Rating = src.Rating
		dst.Why = src.Why
		dst.Findings = src.Findings
		dst.CarriedFrom = src.ID
		dst.CarriedSim = c.sim
		taken[c.oldID] = true
		res.Carried++
		if len(src.Findings) > 0 {
			res.CarriedFindings++
		}
	}

	res.Fresh = res.Total - res.Carried
	res.Lost = len(byID) - res.Carried
	return res
}
