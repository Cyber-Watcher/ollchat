package graph

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Обзор темы по резюме сообществ.
//
// Чем отличается от graph_search. Тот отвечает на вопрос «как связаны X и Y»:
// даёт понятия, связи и цитаты. Обзор отвечает на другой вопрос — «что
// библиотека вообще говорит про X»: даёт **темы** с их описаниями и книгами,
// в которых они разобраны. Разница как между «покажи мне абзац» и «расскажи,
// из чего вообще состоит предмет».
//
// Почему без цитат. Резюме сообществ написаны моделью, а не выписаны из книг,
// и выдавать их за цитаты нельзя. Обзор честно говорит, чем является: картой
// тем со ссылками на книги, где эти темы разобраны. За словами из книги —
// kb_search, и обзор прямо на него указывает.

// OverviewOpts — как строить обзор.
type OverviewOpts struct {
	// TopCommunities — сколько тем показать. 0 — пять.
	TopCommunities int

	// QueryVector — вектор вопроса для смыслового входа; пусто — вход только
	// по написанию. Смысл тот же, что у SearchOpts.QueryVector.
	QueryVector []int8

	// MinRating — не показывать темы с оценкой ниже. 0 — пять.
	//
	// Оценку выставляет модель при написании резюме: насколько тема важна
	// для понимания предмета. В MS GraphRAG глобальный поиск отбирает
	// сообщества ровно по ней — `WHERE c.rating >= $rating_threshold`
	// (Essential GraphRAG, 2025, стр. 127), и порог там пять.
	//
	// До 26.08.2026 оценка считалась и никем не читалась. Это делало
	// бессмысленным и сито по связности (--graph-recheck): оно честно снижало
	// оценку бессвязному набору понятий с 8 до 3, а обзор всё равно показывал
	// его наравне с настоящими темами.
	//
	// Темы без резюме (оценки нет вовсе) порог не отсекает: у них нечего
	// сравнивать, и молча прятать их — значит скрывать от человека, что
	// резюме ещё не написаны.
	MinRating int

	// MinMembers — не показывать сообщества меньше этого размера. 0 — пять.
	MinMembers int
}

func (o OverviewOpts) norm() OverviewOpts {
	if o.TopCommunities <= 0 {
		o.TopCommunities = 5
	}
	if o.MinRating <= 0 {
		o.MinRating = 5
	}
	if o.MinMembers <= 0 {
		o.MinMembers = 5
	}
	return o
}

// OverviewTopic — одна тема в обзоре.
type OverviewTopic struct {
	Community
	// Hits — понятия из вопроса, попавшие в это сообщество. По ним видно,
	// почему тема выбрана, и человек может проверить выбор.
	Hits []string
	// Score — вес темы для этого вопроса.
	Score float64
}

// OverviewResult — что вышло.
type OverviewResult struct {
	Topics []OverviewTopic
	// Linked — понятия вопроса, найденные в графе.
	Linked []string
	// Note — оговорка: сообществ нет, резюме не написаны, охват графа мал.
	Note string
}

// Overview собирает обзор темы по резюме сообществ.
func (g *Graph) Overview(query string, c *Communities, opt OverviewOpts) OverviewResult {
	opt = opt.norm()
	var res OverviewResult

	if c == nil || len(c.List) == 0 {
		res.Note = "сообщества не размечены: ollchat --graph-communities <коллекция>"
		return res
	}

	sopt := g.applyRules(SearchOpts{QueryVector: opt.QueryVector}).norm()
	seeds := g.addSenseSeeds(g.linkEntities(query, sopt), sopt)
	if len(seeds) == 0 {
		res.Note = "в графе нет понятий из этого вопроса"
		return res
	}

	// Понятие → сообщество уровня 0. Строится один раз: перебирать список
	// сообществ на каждое понятие — это квадрат там, где хватает карты.
	// Только нулевой уровень. Объединения верхнего уровня нумеруются
	// независимо, и номера двух уровней накладываются друг на друга:
	// сообщество #5 бывает и мелким, и объединением. Смешав их в одной карте,
	// обзор выдавал тему на 1 715 понятий там, где предел 200.
	where := make(map[uint32]int, c.Entities)
	byID := make(map[int]Community, len(c.List))
	for _, com := range c.List {
		if com.Level != 0 {
			continue
		}
		byID[com.ID] = com
		for _, m := range com.Members {
			where[m] = com.ID
		}
	}

	score := map[int]float64{}
	hits := map[int][]string{}
	for _, s := range seeds {
		res.Linked = append(res.Linked, s.Name)
		id, ok := where[s.ID]
		if !ok {
			// Понятие редкое и в размеченные темы не попало — но тема,
			// в которой оно живёт, всё равно известна: через соседей.
			// Без этого обзор отвечал «понятия нашлись, но ни одно не попало
			// в темы» на вопросы, где смысловой вход находил не главное имя
			// понятия, а его редкое написание («document-reranking» вместо
			// «reranking»). Вес меньше собственного, но больше соседского:
			// это единственная ниточка, а не добавка к прямому попаданию.
			for _, n := range g.neighborsOf(s.ID, 8, nil, NeighborRank{}) {
				if nid, ok := where[n.ID]; ok {
					score[nid] += 0.5
					hits[nid] = append(hits[nid], s.Name)
				}
			}
			continue
		}
		// Вес понятия — его распространённость: понятие из сорока книг
		// говорит о теме больше, чем встреченное дважды.
		score[id] += 1 + float64(s.Books)
		hits[id] = append(hits[id], s.Name)

		// Соседи тянут за собой смежные темы: вопрос про «переранжирование»
		// должен приводить и к теме «поиск в RAG», где оно живёт.
		for _, n := range g.neighborsOf(s.ID, 8, nil, NeighborRank{}) {
			if nid, ok := where[n.ID]; ok && nid != id {
				score[nid] += 0.25
			}
		}
	}

	ids := make([]int, 0, len(score))
	for id := range score {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if score[ids[i]] != score[ids[j]] {
			return score[ids[i]] > score[ids[j]]
		}
		// При равном весе вперёд идёт тема, которую модель сочла важнее:
		// вес говорит «вопрос про это», оценка — «это стоит знать».
		ri, rj := byID[ids[i]].Rating, byID[ids[j]].Rating
		if ri != rj {
			return ri > rj
		}
		return ids[i] < ids[j] // и уже при равной оценке — по номеру, ради устойчивости
	})

	described, cut := 0, 0
	for _, id := range ids {
		com := byID[id]
		if len(com.Members) < opt.MinMembers {
			continue
		}
		// Оценка есть только у описанных тем; ноль означает «резюме не писали».
		if com.Rating > 0 && com.Rating < opt.MinRating {
			cut++
			continue
		}
		if com.Title != "" {
			described++
		}
		res.Topics = append(res.Topics, OverviewTopic{
			Community: com, Hits: hits[id], Score: score[id],
		})
		if len(res.Topics) >= opt.TopCommunities {
			break
		}
	}

	switch {
	case len(res.Topics) == 0 && cut > 0:
		res.Note = fmt.Sprintf("темы по вопросу нашлись (%d), но все они признаны "+
			"незначащими — набор понятий без общей темы", cut)
	case len(res.Topics) == 0:
		res.Note = "понятия вопроса нашлись, но ни одно не попало в размеченные темы"
	case described == 0:
		res.Note = "темы найдены, но резюме к ним не написаны: " +
			"ollchat --graph-summaries <коллекция>"
	}
	return res
}

// RenderOverview печатает обзор для модели и для человека.
func RenderOverview(res OverviewResult, books func(string) string) string {
	if len(res.Topics) == 0 {
		if res.Note != "" {
			return res.Note
		}
		return "по этому вопросу тем в графе не нашлось"
	}

	var b strings.Builder
	if len(res.Linked) > 0 {
		fmt.Fprintf(&b, "Вопрос связан с понятиями: %s\n\n", strings.Join(res.Linked, ", "))
	}
	for i, t := range res.Topics {
		title := t.Title
		if title == "" {
			title = "без названия"
		}
		if t.Rating > 0 {
			fmt.Fprintf(&b, "%d. %s (понятий %d, важность %d из 10)\n",
				i+1, title, len(t.Members), t.Rating)
		} else {
			fmt.Fprintf(&b, "%d. %s (понятий %d)\n", i+1, title, len(t.Members))
		}
		if t.Summary != "" {
			fmt.Fprintf(&b, "   %s\n", t.Summary)
		}
		if len(t.Key) > 0 {
			fmt.Fprintf(&b, "   ключевые понятия: %s\n", strings.Join(t.Key, ", "))
		}
		if len(t.Hits) > 0 {
			fmt.Fprintf(&b, "   из вопроса сюда попало: %s\n", strings.Join(t.Hits, ", "))
		}
		if books != nil && len(t.Books) > 0 {
			var names []string
			for _, d := range t.Books {
				if n := books(d); n != "" {
					names = append(names, n)
				}
			}
			if len(names) > 0 {
				fmt.Fprintf(&b, "   разобрано в книгах: %s\n", strings.Join(names, "; "))
			}
		}
		b.WriteString("\n")
	}
	if res.Note != "" {
		fmt.Fprintf(&b, "%s\n", res.Note)
	}
	b.WriteString("Это карта тем, а не цитаты: описания написаны по графу. " +
		"За словами из книг — kb_search по названным темам.")
	return b.String()
}

// Topic находит тему по номеру или названию.
//
// Нужна инструменту graph_topic: обзор показывает карту тем и их номера,
// а подробный разбор отдаётся отдельным вызовом. Так окно контекста платит
// за выводы только тогда, когда они действительно нужны, — причины и замеры
// в internal/graph/findings.go.
func (c *Communities) Topic(ref string) (Community, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Community{}, false
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(ref, "#")); err == nil {
		return c.Get(n)
	}
	// По названию: сперва точное совпадение, потом вхождение. Точное вернее,
	// а вхождение спасает, когда название переписали своими словами.
	low := strings.ToLower(ref)
	var loose Community
	var found bool
	for _, com := range c.List {
		t := strings.ToLower(com.Title)
		if t == "" {
			continue
		}
		if t == low {
			return com, true
		}
		if !found && strings.Contains(t, low) {
			loose, found = com, true
		}
	}
	return loose, found
}

// RenderTopic печатает одну тему целиком, вместе с разбором.
func RenderTopic(com Community, books func(string) string) string {
	var b strings.Builder
	title := com.Title
	if title == "" {
		title = "без названия"
	}
	fmt.Fprintf(&b, "Тема #%d: %s\n", com.ID, title)
	fmt.Fprintf(&b, "понятий %d", len(com.Members))
	if com.Rating > 0 {
		fmt.Fprintf(&b, ", важность %d из 10", com.Rating)
	}
	b.WriteString("\n")
	if com.Summary != "" {
		fmt.Fprintf(&b, "\n%s\n", com.Summary)
	}
	if com.Why != "" {
		fmt.Fprintf(&b, "\nПочему такая важность: %s\n", com.Why)
	}
	if len(com.Key) > 0 {
		fmt.Fprintf(&b, "\nКлючевые понятия: %s\n", strings.Join(com.Key, ", "))
	}
	if len(com.Findings) > 0 {
		b.WriteString("\nЧто известно по теме:\n")
		for i, f := range com.Findings {
			fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, f.Title, f.Text)
		}
	} else {
		// Молчать об отсутствии разбора нельзя: иначе тема без выводов
		// выглядит темой, по которой книгам нечего сказать.
		b.WriteString("\nРазбор по этой теме не написан " +
			"(ollchat --graph-findings <коллекция>).\n")
	}
	if len(com.Books) > 0 && books != nil {
		var names []string
		for _, id := range com.Books {
			if n := books(id); n != "" {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "\nКниги: %s\n", strings.Join(names, "; "))
		}
	}
	b.WriteString("\nЭто разбор по графу понятий, а не цитаты из книг. " +
		"За словами из книги — kb_search.\n")
	return b.String()
}
