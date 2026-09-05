package graph

import (
	"fmt"
	"sort"
)

// Насколько разбиение отстало от графа.
//
// **Зачем.** Пересчёт сообществ стоит секунд, а вот всё, что после него, —
// полутора часов карты: разбиение строится заново, и названия, резюме, оценки
// и выводы к нему не переносятся (см. этап 79 плана проекта). Поэтому
// пересчитывать после каждой доложенной книги нельзя, а откладывать бесконечно
// тоже нельзя — карта тем начинает врать.
//
// Косвенные признаки вроде «граф вырос на 13%» тут не годятся: рост в стороне
// от размеченных тем не меняет ничего, а рост внутри них перекраивает всё.
// Считать надо прямо — благо разбиение дешёвое: построить новое **в памяти**,
// никуда не сохраняя, и сравнить с нынешним.
//
// Мера сходства — доля общих понятий (Жаккар). Она же понадобится переносу
// названий: сначала показать, много ли изменится, потом перенести описание
// туда, где не изменилось.

// Drift — во что обойдётся пересчёт разбиения прямо сейчас.
type Drift struct {
	// Themes — сколько описанных тем сейчас (уровень 0, с названием).
	Themes int

	// Kept — сколько из них нашли бы себе почти такое же новое сообщество.
	// Их описания при переносе уцелели бы.
	Kept int

	// Changed — сколько перекроились: слились, распались или сменили состав.
	Changed int

	// Uncovered — понятий, не попавших ни в одно нынешнее сообщество.
	// Это и есть прирост графа со времени последнего разбиения.
	Uncovered int

	// Entities — понятий в графе сейчас, и сколько их было при разбиении.
	Entities, EntitiesThen int
	Edges, EdgesThen       int

	// Similarity — порог, при котором тема считалась сохранившейся.
	Similarity float64
}

// Ratio — доля тем, которые пересчёт оставил бы почти как есть.
func (d Drift) Ratio() float64 {
	if d.Themes == 0 {
		return 0
	}
	return float64(d.Kept) / float64(d.Themes)
}

// Verdict — короткий человеческий ответ на «пора ли».
//
// Пороги здесь — правило большого пальца, а не замер, и названы своим именем.
// Замерить «при каком расхождении карта тем начинает врать» можно только
// прогоном вопросов до и после пересчёта; пока такого замера нет, решение
// остаётся за человеком, а команда даёт ему числа.
func (d Drift) Verdict() string {
	if d.Themes == 0 {
		return "тем ещё нет: ollchat --graph-communities"
	}
	// Долю называем числом, а не присказкой: «примерно каждая пятая» при 11%
	// перекроившихся — это не оговорка, а неверное утверждение.
	changed := 100 * float64(d.Changed) / float64(d.Themes)
	switch r := d.Ratio(); {
	case r >= 0.9:
		return fmt.Sprintf("пересчитывать рано: перекроилось бы %.0f%% тем", changed)
	case r >= 0.7:
		return fmt.Sprintf("можно подождать: перекроилось бы %.0f%% тем", changed)
	default:
		return fmt.Sprintf("пора пересчитывать: перекроилось бы %.0f%% тем — "+
			"карта тем заметно разошлась с графом", changed)
	}
}

// DriftOf сравнивает нынешнее разбиение с тем, какое получилось бы сейчас.
//
// Новое разбиение считается в памяти и никуда не сохраняется: команда обязана
// быть безобидной, иначе ею перестанут пользоваться из осторожности.
func (g *Graph) DriftOf(cur *Communities, opt CommunityOpts, similarity float64) (Drift, error) {
	if similarity <= 0 {
		similarity = 0.7
	}
	d := Drift{Similarity: similarity, Entities: g.Entities().Count(), Edges: g.Edges().Count()}
	if cur == nil || len(cur.List) == 0 {
		return d, nil
	}
	d.EntitiesThen, d.EdgesThen = cur.Entities, cur.Edges

	// Понятия вне нынешних тем — прирост со времени разбиения.
	inCur := make(map[uint32]bool)
	for _, com := range cur.List {
		if com.Level != 0 {
			continue
		}
		for _, m := range com.Members {
			inCur[m] = true
		}
	}
	for _, e := range g.Entities().All() {
		if !inCur[e.ID] {
			d.Uncovered++
		}
	}

	fresh, err := g.PartitionOnly(opt)
	if err != nil {
		return d, err
	}

	// Новые сообщества по понятиям: для каждого нового — его состав.
	freshOf := make(map[uint32]int) // понятие → номер нового сообщества
	freshSize := make(map[int]int)
	for _, com := range fresh.List {
		if com.Level != 0 {
			continue
		}
		freshSize[com.ID] = len(com.Members)
		for _, m := range com.Members {
			freshOf[m] = com.ID
		}
	}

	for _, com := range cur.List {
		if com.Level != 0 || com.Title == "" {
			continue
		}
		d.Themes++
		// Куда разошлись понятия старой темы: считаем пересечение с каждым
		// новым сообществом и берём лучшее.
		hits := map[int]int{}
		for _, m := range com.Members {
			if id, ok := freshOf[m]; ok {
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
			d.Changed++
			continue
		}
		// Жаккар: общее делить на объединение.
		union := len(com.Members) + freshSize[bestID] - best
		if union <= 0 {
			d.Changed++
			continue
		}
		if float64(best)/float64(union) >= similarity {
			d.Kept++
		} else {
			d.Changed++
		}
	}
	return d, nil
}

// DriftTopics — самые сильно перекроившиеся темы, чтобы посмотреть глазами.
//
// Числа говорят «сколько», а понять «почему» можно только увидев, во что
// именно рассыпалась тема.
func (g *Graph) DriftTopics(cur *Communities, opt CommunityOpts, limit int) ([]DriftTopic, error) {
	if cur == nil || limit <= 0 {
		return nil, nil
	}
	fresh, err := g.PartitionOnly(opt)
	if err != nil {
		return nil, err
	}
	freshOf := make(map[uint32]int)
	freshSize := make(map[int]int)
	for _, com := range fresh.List {
		if com.Level != 0 {
			continue
		}
		freshSize[com.ID] = len(com.Members)
		for _, m := range com.Members {
			freshOf[m] = com.ID
		}
	}
	var out []DriftTopic
	for _, com := range cur.List {
		if com.Level != 0 || com.Title == "" {
			continue
		}
		hits := map[int]int{}
		for _, m := range com.Members {
			if id, ok := freshOf[m]; ok {
				hits[id]++
			}
		}
		best, bestID := 0, 0
		for id, n := range hits {
			if n > best {
				best, bestID = n, id
			}
		}
		sim := 0.0
		if best > 0 {
			if union := len(com.Members) + freshSize[bestID] - best; union > 0 {
				sim = float64(best) / float64(union)
			}
		}
		out = append(out, DriftTopic{
			ID: com.ID, Title: com.Title, Members: len(com.Members),
			Similarity: sim, Pieces: len(hits),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity < out[j].Similarity })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// DriftTopic — одна тема и её судьба при пересчёте.
type DriftTopic struct {
	ID         int
	Title      string
	Members    int
	Similarity float64 // с самым похожим новым сообществом
	Pieces     int     // на сколько новых сообществ разошлись её понятия
}
