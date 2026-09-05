package graph

import (
	"math"
	"sort"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Поиск по графу.
//
// Отличие от обычного поиска по книгам — в том, что ищется. Обычный ищет куски,
// похожие на вопрос, и ничего не знает о связях между ними. Этот сперва находит
// **понятия**, о которых спрашивают, потом смотрит, с чем они связаны, и лишь
// затем идёт за подтверждениями в книги.
//
// Порядок такой:
//
//  1. связывание вопроса с понятиями — по именам и синонимам;
//  2. расширение на шаг по весам связей: соседи, о которых книги говорят чаще;
//  3. сбор подтверждений — куски, где эти понятия названы;
//  4. выдача, где у каждой связи и каждой цитаты есть книга и страница.
//
// Без четвёртого пункта всё остальное бессмысленно: ответ по книгам без ссылок
// ничем не отличается от выдумки.

// SearchOpts — как искать.
type SearchOpts struct {
	// TopEntities — сколько понятий брать за основу ответа.
	TopEntities int
	// TopNeighbors — сколько соседей показывать у каждого понятия.
	TopNeighbors int
	// TopChunks — сколько кусков-подтверждений собрать.
	TopChunks int
	// MinMentions — не брать в основу понятия, встреченные реже. Ноль — брать
	// любые. Помогает отсечь длинный хвост: 59% понятий встречаются ровно раз.
	MinMentions int

	// Rank — необязательный отбор подтверждений по смыслу вопроса.
	//
	// Без него куски выбираются по числу упоминаний найденных понятий,
	// и у книги выигрывает не то, что отвечает на вопрос, а то, где понятия
	// стоят гуще всего. Замер 23.08.2026 на живом графе: вопрос про сообщества
	// и извлечение сущностей в GraphRAG получил в подтверждения страницы 2, 6
	// и 6–7 — отзывы на обложке и «кому эта книга», где перечислены все
	// понятия разом. Готовая реализация — graph.RankWith(коллекция).
	Rank RankFunc

	// Rank настроек ранжирования связей — см. NeighborRank. Нулевое значение
	// означает «как решено по умолчанию», а не «выключено»: умолчания собраны
	// в DefaultNeighborRank, чтобы вызывающему не приходилось их повторять.
	Neighbors NeighborRank

	// Groups — как применять группы понятий: "union", "expand" или "off".
	// Пусто — как в настройках (GroupMode при norm). Разбор — в groups.go.
	Groups string

	// QueryVector — вектор вопроса, посчитанный тем же эмбеддером, каким
	// считались векторы понятий. Пусто — вход только по написанию.
	//
	// Вектор передаётся снаружи, а не считается здесь, по той же причине,
	// что и Rank: пакет графа не ходит в сеть. Кто зовёт поиск, тот и решает,
	// доступен ли сейчас сервер эмбеддингов и стоит ли ждать ответа.
	QueryVector []int8
}

// applyRules подставляет правила графа туда, где вызывающий не задал своего:
// сейчас это режим групп.
func (g *Graph) applyRules(o SearchOpts) SearchOpts {
	if o.Groups == "" {
		o.Groups = g.rules.Groups
	}
	return o
}

// RankFunc отбирает из кандидатов те куски, что ближе к вопросу по словам.
type RankFunc func(query string, cands []ChunkKey, limit int) []ChunkKey

func (o SearchOpts) norm() SearchOpts {
	switch o.Groups {
	case GroupOff, GroupUnion, GroupExpand:
	default:
		o.Groups = GroupOff
	}
	if o.TopEntities <= 0 {
		o.TopEntities = 6
	}
	if o.TopNeighbors <= 0 {
		o.TopNeighbors = 5
	}
	if o.TopChunks <= 0 {
		o.TopChunks = 6
	}
	return o
}

// NeighborInfo — сосед понятия с уже разрешённым именем.
//
// Отдельный тип, а не Neighbor: тот живёт в хранилище связей и имён не знает
// вовсе — там только номера. Имена подставляются здесь, один раз, чтобы
// печатающему коду не пришлось таскать с собой весь граф.
type NeighborInfo struct {
	ID     uint32
	Name   string
	Rel    string // тип связи, самый частый из встреченных
	Weight float32
	Count  int  // сколькими кусками связь подтверждена
	In     bool // связь идёт от соседа к нам: печатать её надо в обратную сторону
}

// FoundEntity — понятие, найденное по вопросу.
type FoundEntity struct {
	Entity
	Mentions int    // сколько раз встречается в библиотеке
	Books    int    // в скольких книгах
	Matched  string // каким написанием совпало с вопросом
	// Aliases — синонимы для показа человеку и модели: всё пригодное, переводы
	// впереди. Заполняется при поиске, потому что отбор требует реестра,
	// а форматирование его не видит.
	Aliases []string

	// AliasesSafe — то же без синонимов, которые являются собственным именем
	// другого понятия. Идёт в расширение запроса к книгам: чужое имя там
	// подмешивает чужой термин («goroutine, он же Go» вытянул бы книги про
	// язык). Замер цены обоих списков — на эталоне aliases_sample.tsv,
	// разбор в GraphHealth.md.
	AliasesSafe []string

	Neighbors []NeighborInfo // с чем связано, от прочного к слабому
}

// FoundRelation — связь между двумя найденными понятиями.
type FoundRelation struct {
	Src, Dst string
	Type     string
	Weight   float32
	Count    int
	Evidence ChunkKey

	// Evidences — ещё несколько кусков, подтверждающих ту же связь.
	//
	// **Зачем не один.** Связь `Go —использует→ Garbage collection` подтверждена
	// 186 кусками, а показывали всегда первый попавшийся — и им оказывалась
	// шпаргалка по приведению типов, где оба имени просто стоят рядом в списке.
	// Отрисовка выбирает из нескольких тот кусок, где рядом стоят оба конца
	// связи, то есть где книга их и связывает.
	//
	// Список короткий намеренно: каждый кусок — обращение к хранилищу, а связей
	// в выдаче дюжина. Первым идёт тот же кусок, что лежит в Evidence.
	Evidences []ChunkKey
}

// maxEvidences — сколько кусков-подтверждений собирать на связь.
//
// Четыре, и это замерено: восемь не улучшили ни одной строки в пробе
// 02.09.2026, а чтений хранилища вдвое больше. У связи-долгожителя вроде
// `Go —использует→ Garbage collection` (186 подтверждений) первые записи идут
// подряд из одной книги, и расширение окна перебора их не разбавляет —
// помогло бы только хранение лучшего подтверждения на стороне сборки.

// SearchResult — что нашлось по вопросу.
type SearchResult struct {
	Entities  []FoundEntity
	Relations []FoundRelation
	// Chunks — куски-подтверждения: сюда смотрит тот, кто хочет проверить.
	Chunks []ChunkKey
	// Note — честное пояснение, когда выдача неполна: граф собран не весь,
	// понятия не нашлись. Пустая строка означает, что оговорок нет.
	Note string
}

// Search ищет по графу.
func (g *Graph) Search(query string, opt SearchOpts) SearchResult {
	opt = g.applyRules(opt).norm()
	var res SearchResult

	seeds := g.linkEntities(query, opt)
	seeds = g.addSenseSeeds(seeds, opt)
	if len(seeds) == 0 {
		res.Note = "в графе нет понятий из этого вопроса"
		return res
	}

	inSeeds := make(map[uint32]bool, len(seeds))
	for _, s := range seeds {
		inSeeds[s.ID] = true
	}

	// Группы понятий: спросили одно из группы — показать и остальных её членов.
	// Узлы не слиты, у каждого свои книги и связи; объединяется только выдача.
	// «union» добавляет членов группы в основу ответа, «off» — не трогает.
	if opt.Groups == "union" {
		seeds = g.addGroupSiblings(seeds, inSeeds, opt)
	}

	// Соседи и связи. Связи между самими найденными понятиями ценнее прочих:
	// именно они отвечают на вопрос «как это связано с тем».
	for i := range seeds {
		seeds[i].Neighbors = g.neighborsOf(seeds[i].ID, opt.TopNeighbors, opt.QueryVector, opt.Neighbors)
		for _, n := range seeds[i].Neighbors {
			// Входящую связь печатаем в её настоящую сторону: сосед → мы.
			// Иначе «горутина —использует→ канал» в карточке канала
			// превратилась бы в «канал —использует→ горутина».
			from, to := seeds[i].Name, n.Name
			fromID, toID := seeds[i].ID, n.ID
			if n.In {
				from, to = n.Name, seeds[i].Name
				fromID, toID = n.ID, seeds[i].ID
			}
			rel := FoundRelation{
				Src: from, Dst: to, Type: n.Rel,
				Weight: n.Weight, Count: n.Count,
			}
			for _, e := range g.edge.Of(fromID) {
				if e.Dst != toID {
					continue
				}
				// Подтверждение из отброшенной книги не показываем.
				if g.dropped.Dropped(e.Evidence.Doc) {
					continue
				}
				if len(rel.Evidences) == 0 {
					rel.Evidence = e.Evidence
				}
				rel.Evidences = append(rel.Evidences, e.Evidence)
				if len(rel.Evidences) >= g.rules.MaxEvidences {
					break
				}
			}
			res.Relations = append(res.Relations, rel)
		}
	}
	// Связи между найденными понятиями — вперёд, они прямее отвечают на вопрос.
	sort.SliceStable(res.Relations, func(i, j int) bool {
		mi := inSeeds[idOf(g, res.Relations[i].Dst)]
		mj := inSeeds[idOf(g, res.Relations[j].Dst)]
		if mi != mj {
			return mi
		}
		return res.Relations[i].Weight > res.Relations[j].Weight
	})

	res.Entities = seeds
	if opt.Rank != nil {
		// Кандидатов берём с запасом: отбор по словам должен иметь из чего
		// выбирать, иначе он лишь переставит те же неудачные куски.
		cands := g.evidence(seeds, opt.TopChunks*8)
		res.Chunks = opt.Rank(query, cands, opt.TopChunks)
	} else {
		res.Chunks = g.evidence(seeds, opt.TopChunks)
	}
	return res
}

// linkEntities связывает вопрос с понятиями графа.
//
// Ищутся сочетания из одного, двух и трёх слов подряд — от длинных к коротким.
// Длинные важнее: «контекстное окно» это одно понятие, а не «контекст» и «окно»
// по отдельности, и если совпало длинное, короткие внутри него уже не нужны.
func (g *Graph) linkEntities(query string, opt SearchOpts) []FoundEntity {
	words := strings.Fields(Normalize(query))
	if len(words) == 0 {
		return nil
	}

	found := map[uint32]FoundEntity{}
	covered := make([]bool, len(words))

	for size := 3; size >= 1; size-- {
		for i := 0; i+size <= len(words); i++ {
			if covered[i] {
				continue
			}
			phrase := strings.Join(words[i:i+size], " ")
			// Сперва точное написание, затем основы слов: вопрос задают живой
			// речью («чем переранжировать»), а понятия записаны словарной
			// формой («переранжирование»).
			ent, ok := g.ents.Lookup(phrase)
			byStem := false
			if !ok {
				ent, ok = g.ents.LookupStem(phrase)
				byStem = ok
			}
			if !ok {
				continue
			}
			// Совпадение по основе слова — догадка, а не совпадение: вопрос
			// написан одной формой, понятие названо другой. На догадку разумно
			// требовать больше подтверждений, чем на точное имя.
			//
			// **Замер 03.09.2026.** Вопросы вида «как связаны X и Y» вытягивали
			// понятие «Связанность» (6 упоминаний в ОДНОЙ книге) в 59 случаях
			// из 60: слово «связаны» совпадает с ним основой. Частота слова тут
			// не помогает — «связаны» встречается в 1.0% кусков, ровно как
			// «goroutine» (1.1%). А число книг помогает: у настоящего предмета
			// вопроса их десятки.
			if byStem && booksOf(g, ent.ID) < g.rules.StemMinBooks {
				continue
			}
			if _, dup := found[ent.ID]; dup {
				continue
			}
			mentions := len(g.ment.Of(ent.ID))
			if opt.MinMentions > 0 && mentions < opt.MinMentions {
				continue
			}
			found[ent.ID] = FoundEntity{
				Entity: ent, Mentions: mentions,
				Aliases:     g.ents.DisplayAliases(ent),
				AliasesSafe: g.ents.SafeAliases(ent),
				Books:       booksOf(g, ent.ID), Matched: phrase,
			}
			for j := i; j < i+size; j++ {
				covered[j] = true
			}
		}
	}

	out := make([]FoundEntity, 0, len(found))
	for _, e := range found {
		out = append(out, e)
	}
	// Сперва то, о чём книги говорят чаще: у такого понятия и связи надёжнее.
	// При равенстве — по имени, чтобы выдача не плясала от запуска к запуску.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mentions != out[j].Mentions {
			return out[i].Mentions > out[j].Mentions
		}
		return out[i].Name < out[j].Name
	})
	return topEntities(out, opt.TopEntities)
}

// evidence собирает куски, подтверждающие найденное.
//
// Кусок, где названы сразу несколько искомых понятий, ценнее куска с одним:
// именно в нём, скорее всего, и написано, как они связаны.
func (g *Graph) evidence(seeds []FoundEntity, limit int) []ChunkKey {
	score := map[uint64]int{}
	for _, s := range seeds {
		for _, k := range g.ment.Of(s.ID) {
			// Кусок отброшенной книги в выдачу не идёт: её вклад скрыт
			// (см. dropbook.go). Реестр и журналы при этом целы.
			if g.dropped.Dropped(k.Doc) {
				continue
			}
			score[k.Pack()]++
		}
	}
	if len(score) == 0 {
		return nil
	}
	type pair struct {
		key uint64
		n   int
	}
	list := make([]pair, 0, len(score))
	for k, n := range score {
		list = append(list, pair{k, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].key < list[j].key // устойчивый порядок
	})
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]ChunkKey, 0, len(list))
	for _, p := range list {
		out = append(out, UnpackChunk(p.key))
	}
	return out
}

// Entity ищет понятие по имени и собирает его карточку.
func (g *Graph) Entity(name string, opt SearchOpts) (FoundEntity, bool) {
	opt = g.applyRules(opt).norm()
	ent, ok := g.ents.Lookup(name)
	if !ok {
		return FoundEntity{}, false
	}
	return FoundEntity{
		Entity:      ent,
		Mentions:    len(g.ment.Of(ent.ID)),
		Books:       booksOf(g, ent.ID),
		Matched:     Normalize(name),
		Aliases:     g.ents.DisplayAliases(ent),
		AliasesSafe: g.ents.SafeAliases(ent),
		Neighbors:   g.neighborsOf(ent.ID, opt.TopNeighbors, opt.QueryVector, opt.Neighbors),
	}, true
}

// neighborsOf собирает соседей с именами, отбирая их по вопросу.
//
// **Зачем понадобилось.** Связи у понятия отсортированы по весу — по числу
// кусков, которыми связь подтверждена, — и показываются первые пять. Вопрос
// при этом не учитывался вовсе: какой бы вопрос ни задали, наверху одни и те же
// пять связей.
//
// Пока связей было мало, это не мешало. Замер 27.08.2026 по 61 понятию из
// живых выдач: **медиана 34 связи на понятие при показе пяти**, у `LLM` их
// 4 421, у `prompt injection` — 186. Скрыто 96% связей, и так было ещё
// до склейки двойников — она лишь вскрыла давнюю болезнь, добавив 19% связей
// и подняв долю скрытого на 0.6 пункта.
//
// Как это выглядело: на вопрос про `cookies` наверх выходил
// `cookie —часть→ FastAPI`, вытесняя `SameSite` и `same-origin policy`.
// У FastAPI подтверждений больше — но не потому, что он отвечает на вопрос,
// а потому, что про него в книгах пишут часто.
//
// **Как выбираются пять.** Берётся пул пошире, считается второе ранжирование —
// по близости вектора соседа к вектору вопроса, — и два списка сливаются
// по местам (RRF).
//
// **Почему по местам, а не по порогу близости.** Абсолютная отсечка здесь уже
// проверена и отвергнута: у заведомо чужой пары близость 0.565, у осмысленной
// «как ускорить выдачу поиска» ↔ Vector search — 0.576, разница в сотую.
// Числа сравнимы только внутри одного списка, места — сравнимы всегда.
//
// **Пустой вектор оставляет всё как было.** Обзор тем зовёт эту функцию, не имея
// вопроса вовсе (`overview.go`): он ищет тему понятия через соседей. Правка,
// его не касающаяся, не должна его сдвинуть.
func (g *Graph) neighborsOf(id uint32, limit int, qv []int8, rank NeighborRank) []NeighborInfo {
	list := g.edge.Neighbors(id) // уже по убыванию веса
	if len(list) == 0 {
		return nil
	}
	list = g.rankNeighbors(list, limit, qv, rank)
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]NeighborInfo, 0, len(list))
	for _, n := range list {
		ent, ok := g.ents.Get(n.ID)
		if !ok {
			continue
		}
		out = append(out, NeighborInfo{
			ID: n.ID, Name: ent.Name, Rel: RelName(firstType(n.Types)),
			Weight: n.Weight, Count: n.Count, In: n.In,
		})
	}
	return out
}

// NeighborRank — как пересортировывать связи с оглядкой на вопрос.
//
// Вынесено в настройки, потому что решение здесь **не универсальное**, а зависит
// от того, как собран граф и о чём его спрашивают: на нашем графе замер сказал
// «выключить», на другом наборе книг ответ может быть иным. Менять поведение
// пересборкой бинаря — плохой способ: у пользователей бинарь готовый.
type NeighborRank struct {
	// SenseWeight — сколько стоит близость связи к вопросу против веса связи.
	// **0 выключает пересортировку**: связи остаются в порядке подтверждённости
	// книгами. Это и есть умолчание — см. замер ниже.
	SenseWeight float64

	// Pool — во сколько раз шире показанного берётся пул для пересортировки.
	// 0 — DefaultNeighborRank.Pool.
	Pool int
}

// DefaultNeighborRank — умолчания ранжирования связей.
//
// **Почему ранжирование выключено.** Замер 28.08.2026 на графе books,
// 100 вопросов, слепое судейство qwen3.5:122b **в оба порядка** (иначе модель
// выбирает вторую выдачу в 85% случаев независимо от содержания):
// из 24 согласных вердиктов 18 в пользу выдачи **без** ранжирования, 5 за,
// одна ничья, p = 0.011. Ранжирование выбивало из выдачи конкретное
// и подтверждённое книгами ради близкого к теме вопроса вообще: `go vet`
// терял связь с `sync.Locker`, `Init function` — с `Unit tests`.
//
// Сужение пула с 8 до 3 вреда не сняло (11:13, ничья), поэтому пул остаётся
// узким: если ранжирование включат настройкой, пусть переставляет уместное,
// а не подменяет треть выдачи.
var DefaultNeighborRank = NeighborRank{SenseWeight: 0, Pool: 3}

func (r NeighborRank) pool() int {
	if r.Pool <= 0 {
		return DefaultNeighborRank.Pool
	}
	return r.Pool
}

// rankNeighbors пересортировывает соседей с оглядкой на вопрос.
//
// Без вектора вопроса возвращает список как есть — по весу.
func (g *Graph) rankNeighbors(list []Neighbor, limit int, qv []int8, rank NeighborRank) []Neighbor {
	if rank.SenseWeight <= 0 || len(qv) == 0 || g.vecs == nil || !g.vecs.Ready() || limit <= 0 {
		return list
	}
	if len(qv) != g.vecs.Dim() {
		return list // вектор посчитан другой моделью — доверять нечему
	}
	pool := limit * rank.pool()
	if len(list) > pool {
		list = list[:pool]
	}
	if len(list) <= limit {
		return list
	}

	// Место по весу — порядок, в котором список пришёл.
	score := make(map[uint32]float64, len(list))
	for i, n := range list {
		score[n.ID] = 1 / (rrfK + float64(i+1))
	}

	// Второе ранжирование — по близости к вопросу. Соседи без вектора
	// в нём не участвуют: у них нет места, а не нулевая близость.
	type scored struct {
		id  uint32
		cos float64
	}
	near := make([]scored, 0, len(list))
	for _, n := range list {
		if vec, ok := g.vecs.vectorOf(n.ID); ok {
			near = append(near, scored{n.ID, kb.Cosine(vec, qv)})
		}
	}
	sort.Slice(near, func(i, j int) bool {
		if near[i].cos != near[j].cos {
			return near[i].cos > near[j].cos
		}
		return near[i].id < near[j].id
	})
	for i, s := range near {
		score[s.id] += rank.SenseWeight / (rrfK + float64(i+1))
	}

	pos := make(map[uint32]int, len(list))
	for i, n := range list {
		pos[n.ID] = i
	}
	out := append([]Neighbor(nil), list...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := score[out[i].ID], score[out[j].ID]
		if a != b {
			return a > b
		}
		return pos[out[i].ID] < pos[out[j].ID] // при равенстве — прежний порядок
	})
	return out
}

// О значении веса уместности, если ранжирование всё-таки включают настройкой.
//
// **Единица не годится, и это не вкус, а арифметика.** Равновесный RRF
// симметричен: сосед, первый по весу и последний по смыслу, даёт ровно ту же
// сумму, что первый по смыслу и последний по весу — 1/(20+1) + 1/(20+6)
// в обе стороны. То есть при равных весах тяжёлый, но чужой вопросу сосед
// НИКОГДА не уступит место уместному, а вся правка окажется бесполезной.
// Поймано тестом, а не рассуждением.
//
// Осмысленный порядок величины — 1.5: столько подобрано замером в поиске
// по книгам (internal/kb/fusion.go, kb_semantic_search.md). На графе это
// значение проверено 28.08.2026 и **проиграло** выдаче без ранжирования —
// см. DefaultNeighborRank. Меньшие значения не мерены.

// rrfK — та же постоянная слияния, что и у поиска по книгам.
//
// Значение общее не для красоты: приём один и тот же — слить два ранжирования
// по местам, — и подобрано оно замером на живой коллекции (internal/kb/fusion.go,
// kb_semantic_search.md). Заводить рядом второе число значило бы подбирать
// его заново без всякой на то причины.
const rrfK = 20.0

// PathStep — шаг на пути между понятиями.
type PathStep struct {
	From, To string
	Type     string
	Weight   float32
	Evidence ChunkKey
}

// Path ищет кратчайшую цепочку связей между двумя понятиями.
//
// Обход в ширину по весам не идёт: нужен **короткий** путь, а не самый прочный.
// Цепочка из двух шагов через часто встречающееся понятие объясняет связь лучше,
// чем цепочка из шести шагов с большими весами — её просто не прочесть.
func (g *Graph) Path(from, to string, maxHops int) ([]PathStep, bool) {
	if maxHops <= 0 {
		maxHops = 4
	}
	a, ok := g.ents.Lookup(from)
	if !ok {
		return nil, false
	}
	b, ok := g.ents.Lookup(to)
	if !ok {
		return nil, false
	}
	if a.ID == b.ID {
		return nil, true
	}

	visited := map[uint32]link{a.ID: {}}
	queue := []uint32{a.ID}

	for hop := 0; hop < maxHops && len(queue) > 0; hop++ {
		var next []uint32
		for _, cur := range queue {
			for _, e := range g.edge.Of(cur) {
				if _, seen := visited[e.Dst]; seen {
					continue
				}
				visited[e.Dst] = link{prev: cur, edge: e}
				if e.Dst == b.ID {
					return buildPath(g, visited, a.ID, b.ID), true
				}
				next = append(next, e.Dst)
			}
		}
		// Порядок обхода устойчив: иначе один и тот же вопрос даёт разные пути.
		sort.Slice(next, func(i, j int) bool { return next[i] < next[j] })
		queue = next
	}
	return nil, false
}

// link — откуда пришли в узел при обходе.
type link struct {
	prev uint32
	edge Edge
}

func buildPath(g *Graph, visited map[uint32]link, from, to uint32) []PathStep {
	var steps []PathStep
	for cur := to; cur != from; {
		link := visited[cur]
		src, _ := g.ents.Get(link.prev)
		dst, _ := g.ents.Get(cur)
		steps = append(steps, PathStep{
			From: src.Name, To: dst.Name, Type: RelName(link.edge.Type),
			Weight: link.edge.Weight, Evidence: link.edge.Evidence,
		})
		cur = link.prev
	}
	// Собирали с конца — разворачиваем.
	for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
		steps[i], steps[j] = steps[j], steps[i]
	}
	return steps
}

// booksOf считает, в скольких книгах встречается понятие.
func booksOf(g *Graph, id uint32) int {
	seen := map[uint32]bool{}
	for _, k := range g.ment.Of(id) {
		seen[k.Doc] = true
	}
	return len(seen)
}

func idOf(g *Graph, name string) uint32 {
	if e, ok := g.ents.Lookup(name); ok {
		return e.ID
	}
	return 0
}

func firstType(types []uint8) uint8 {
	if len(types) == 0 {
		return RelRelated
	}
	return types[0]
}

func topEntities(list []FoundEntity, n int) []FoundEntity {
	if len(list) > n {
		return list[:n]
	}
	return list
}

// addSenseSeeds добавляет ко входу понятия, близкие к вопросу по смыслу.
//
// Лексический вход остаётся первым и главным: точное написание надёжнее
// близости векторов, и понятие, найденное по слову вопроса, стоит выше
// найденного по смыслу. Смысловые добавляются в хвост и только те, которых
// ещё нет, — они закрывают случаи, где написание не совпало вовсе:
// другой язык («выявление сообществ» против «Community detection») и другая
// часть речи («переранжировать» против «переранжирование»).
//
// Число добавленных ограничено половиной TopEntities: смысловая близость
// ошибается чаще словесной, и отдавать ей весь вход нельзя.
func (g *Graph) addSenseSeeds(seeds []FoundEntity, opt SearchOpts) []FoundEntity {
	if len(opt.QueryVector) == 0 || g.vecs == nil || !g.vecs.Ready() {
		return seeds
	}
	room := opt.TopEntities - len(seeds)
	if max := opt.TopEntities / 2; room > max {
		room = max
	}
	if room <= 0 {
		return seeds
	}

	have := make(map[uint32]bool, len(seeds))
	for _, s := range seeds {
		have[s.ID] = true
	}

	// Кандидатов берём с запасом: дальше их порядок пересматривается.
	type cand struct {
		hit      senseHit
		ent      Entity
		mentions int
	}
	var cands []cand
	for _, h := range g.vecs.linkBySense(opt.QueryVector, room*6, g.rules.SenseMargin) {
		if have[h.ID] {
			continue
		}
		ent, ok := g.ents.Get(h.ID)
		if !ok {
			continue
		}
		// После склейки два разных вектора ведут к одному понятию: у каждой
		// половины был свой. Проверять надо номер выжившего, иначе он придёт
		// в выдачу дважды и вытеснит собой что-то другое.
		if have[ent.ID] {
			continue
		}
		have[ent.ID] = true
		h.ID = ent.ID
		mentions := len(g.ment.Of(h.ID))
		if opt.MinMentions > 0 && mentions < opt.MinMentions {
			continue
		}
		cands = append(cands, cand{h, ent, mentions})
	}

	// Близость сама по себе выбирает плохо. Замер 26.08.2026 на живом графе:
	// вопрос «чем переранжировать найденные куски» дал «reranked chunk» 0.667
	// (одно упоминание в одной книге), «document-reranking» 0.632 (тоже одно)
	// и «reranking» 0.633 — при 159 упоминаниях в 12 книгах. Разброс в три
	// сотых — это шум, а наверх по нему всплывало редкое частное имя, и вход
	// в граф уводил в сторону от того, о чём книги на самом деле говорят.
	//
	// Поэтому близости огрубляются до ступеней, а внутри ступени выигрывает
	// то, что чаще упоминается, — тот же принцип, что и при отборе цитат.
	sort.SliceStable(cands, func(i, j int) bool {
		bi := math.Floor(cands[i].hit.Score / g.rules.SenseTie)
		bj := math.Floor(cands[j].hit.Score / g.rules.SenseTie)
		if bi != bj {
			return bi > bj
		}
		if cands[i].mentions != cands[j].mentions {
			return cands[i].mentions > cands[j].mentions
		}
		return cands[i].hit.ID < cands[j].hit.ID
	})

	for _, c := range cands {
		if room <= 0 || len(seeds) >= opt.TopEntities {
			break
		}
		have[c.hit.ID] = true
		seeds = append(seeds, FoundEntity{
			Entity: c.ent, Mentions: c.mentions,
			Aliases:     g.ents.DisplayAliases(c.ent),
			AliasesSafe: g.ents.SafeAliases(c.ent),
			Books:       booksOf(g, c.hit.ID), Matched: "по смыслу",
		})
		room--
	}
	return seeds
}

// senseTie — ступень огрубления смысловой близости.
//
// Всё, что попало в одну ступень, считается одинаково близким, и выбор внутри
// неё решает распространённость понятия. Размер взят по замеру выше: разброс
// между главным понятием и его частными написаниями там был около трёх сотых,
// то есть внутри одной ступени.
