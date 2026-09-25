package kb

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Замер качества поиска.
//
// Зачем он вообще. До 26.08.2026 о качестве поиска по библиотеке было известно
// ровно одно: он что-то находит. Этого мало — «что-то находится» не отличает
// выдачу, где нужный кусок стоит первым, от выдачи, где он восьмой, и не даёт
// сравнить два способа искать. Всякое «стало лучше» без такого замера остаётся
// верой.
//
// Меряются три вещи, принятые в отрасли:
//
//	recall@k — нашёлся ли нужный кусок в первых k;
//	MRR      — насколько высоко он стоит (1/место первого попадания);
//	nDCG@k   — то же, но с плавным падением цены места.
//
// Судить «нужный или нет» можно по двум признакам, и оба честные по-своему.
// По **номеру куска** — вопрос составлен по этому куску, и правильный ответ
// один; так меряется точность попадания. По **книге и странице** — правильным
// считается любой кусок нужного места книги; так меряются вопросы, заданные
// человеком, у которых верных источников несколько.

// EvalCase — один вопрос замерного набора.
type EvalCase struct {
	Query string `toml:"query"`

	// ChunkID — «books/12#37», кусок, по которому вопрос составлен. Если задан,
	// попаданием считается ровно он.
	ChunkID string `toml:"chunk_id"`

	// Book — часть названия книги или пути к ней. Если ChunkID пуст, попаданием
	// считается любой кусок из этой книги.
	Book string `toml:"book"`

	// Pages сужает Book до нужных страниц: попаданием считается кусок,
	// накрывающий любую из них. Пусто — вся книга целиком.
	Pages []int `toml:"pages"`

	// Entity — понятие графа, которое обязан найти graph_search по этому
	// вопросу. Для замера поиска по книгам не нужно.
	Entity string `toml:"entity"`

	// Must — что обязано прозвучать в ответе модели, если найденный кусок
	// был прочитан и использован (этап 101, A5). Проверяется подстрокой без
	// учёта регистра; пусто — случай меряет только извлечение.
	//
	// Отдельно от ChunkID намеренно: извлечение и интеграция — разные числа,
	// и смешивать их значит не знать, что именно сдвинулось.
	Must []string `toml:"must"`

	// Note — зачем вопрос в наборе, для человека.
	Note string `toml:"note"`
}

// hit решает, засчитывать ли этот кусок за правильный ответ.
func (c EvalCase) hit(r Result) bool {
	if c.ChunkID != "" {
		return r.ID == c.ChunkID
	}
	if c.Book == "" {
		return false
	}
	if !containsFold(r.Book, c.Book) && !containsFold(r.Path, c.Book) {
		return false
	}
	if len(c.Pages) == 0 {
		return true
	}
	for _, p := range c.Pages {
		// Границы куска и страницы в книге совпадают редко, поэтому засчитываем
		// перекрытие, а не равенство.
		if p >= r.UnitFrom && p <= r.UnitTo {
			return true
		}
	}
	return false
}

// EvalMode — какой поиск меряем.
type EvalMode string

const (
	EvalLexical  EvalMode = "слова"   // только BM25
	EvalSemantic EvalMode = "векторы" // только векторы
	EvalFusion   EvalMode = "слияние" // как в работе: RRF по обоим спискам
)

// EvalModes — порядок, в котором их показывать.
var EvalModes = []EvalMode{EvalLexical, EvalSemantic, EvalFusion}

// EvalReport — итог замера по одному режиму.
type EvalReport struct {
	Mode    EvalMode
	Cases   int
	Recall  float64 // доля вопросов, где нужный кусок попал в первые K
	MRR     float64
	NDCG    float64
	Missed  []string // вопросы, где не нашлось вовсе
	AvgRank float64  // среднее место попадания среди найденных

	// Dropped — вопросы, где нужный кусок достижим при щедром бюджете
	// (WideK, предел на книгу снят), но в рабочую выдачу не попал. Это не
	// промах поиска, а нехватка места: лечится `top_k` и `max_per_book`.
	// Считается только при WideK > 0; такие вопросы остаются и в Missed —
	// рабочая выдача их действительно не нашла, и recall от этого не меняется.
	Dropped []string

	// Вторая мера попадания — «та же страница» (этап 105, З4).
	//
	// **Зачем она рядом со строгой.** Строгая засчитывает РОВНО тот кусок,
	// по которому составлен вопрос. Между тем нарезка идёт с перекрытием,
	// а доводка выдачи намеренно выбрасывает соседние куски: если ответ достался
	// человеку в соседнем куске той же страницы, строгая мера считает это
	// промахом поиска. Замер 24.09.2026: таких «промахов» четверть при k = 10,
	// и починка меры даёт 73 вопроса из 452 против 23 от выключения самой
	// доводки — то есть наказывал прибор, а не поиск.
	//
	// Попаданием по этой мере считается кусок ТОЙ ЖЕ книги, накрывающий
	// страницу эталона (а если страницы не прочитать — соседний по номеру,
	// не дальше NearOrds). Строгие числа остаются рядом и не меняются:
	// менять прибор посреди замеров нельзя, обе меры печатаются вместе.
	RecallNear  float64
	MRRNear     float64
	NDCGNear    float64
	AvgRankNear float64

	// NearBook — промахи, где в выдаче была ТА ЖЕ КНИГА, что у эталонного куска,
	// NearChunk — где ещё и кусок рядом с эталонным (не дальше NearOrds номеров).
	//
	// **Зачем.** Набор засчитывает ровно тот кусок, по которому составлен
	// вопрос, а на вопрос в библиотеке отвечают десятки кусков; вдобавок
	// доводка выдачи намеренно выбрасывает СОСЕДНИЕ куски (нарезка идёт
	// с перекрытием). Значит часть «промахов» — промахи прибора: ответ
	// человеку и модели достался, а замер его не увидел. Эти два числа
	// и отделяют одно от другого (этап 105, «беда в отборе»).
	NearBook  int
	NearChunk int

	// NearDist — расстояние в номерах кусков до эталона у тех промахов, где
	// нашлась та же книга: по нему видно, «сосед» это или другая глава.
	NearDist []int

	// DroppedInK — сколько из Dropped стояли в щедром списке не ниже K-го
	// места. У них места хватило бы и при нынешнем top_k, значит срезал
	// предел на книгу; у остальных — сам top_k.
	DroppedInK int

	// Gaps — разрыв первого и второго места по каждому вопросу и попал ли
	// нужный кусок в первые K. По ним подбирается порог воздержания
	// (этап 91, R2.11): ниже какого разрыва честнее сказать «в книгах нет».
	Gaps []GapPoint
}

// GapPoint — один вопрос в замере воздержания.
type GapPoint struct {
	Gap  float64 // (score₁ − score₂) / score₁; 0, если кусков меньше двух
	Top1 float64 // оценка первого места как есть: у реранкера шкала сопоставима между запросами
	Hit  bool    // нужный кусок был в выдаче
}

// AbstainRow — что даёт порог воздержания на наборе.
type AbstainRow struct {
	Threshold float64
	// Silent — сколько вопросов промолчали бы (разрыв ниже порога).
	Silent int
	// SilentRight — из них таких, где нужного куска и не было: молчание честное.
	SilentRight int
	// SilentWrong — из них таких, где нужный кусок был: потеря.
	SilentWrong int
}

// AbstainTable считает, что даёт каждый порог разрыва: сколько вопросов
// промолчали бы, сколько из них честно (верного куска не было) и сколько
// зря (был). Порог выбирается по этим двум числам, а не назначается.
func AbstainTable(points []GapPoint, thresholds []float64) []AbstainRow {
	return abstainTableBy(points, thresholds, func(p GapPoint) float64 { return p.Gap })
}

// AbstainTableTop1 — то же по абсолютной оценке первого места. Имеет смысл
// только на шкале реранкера: оценки слияния по рангам от запроса к запросу
// несопоставимы (этап 89, шаг 4).
func AbstainTableTop1(points []GapPoint, thresholds []float64) []AbstainRow {
	return abstainTableBy(points, thresholds, func(p GapPoint) float64 { return p.Top1 })
}

// goldRef — эталонный кусок вопроса: книга, номер куска и его страницы.
// Страницы читаются у коллекции; если куска в ней нет (замер идёт против
// другой коллекции или подставного поиска), остаётся сравнение по номерам.
type goldRef struct {
	doc, ord int
	from, to int // страницы эталона; 0 — неизвестны
}

// near — тот же кусок, кусок той же страницы или соседний по номеру.
func (g goldRef) near(r Result, ords int) bool {
	doc, ord, ok := splitChunkID(r.ID)
	if !ok || doc != g.doc {
		return false
	}
	if g.from > 0 && r.UnitTo >= g.from && r.UnitFrom <= g.to {
		return true // страницы перекрываются
	}
	d := ord - g.ord
	if d < 0 {
		d = -d
	}
	return d <= ords
}

// goldSpan собирает эталон вопроса. Пусто, когда в наборе нет ссылки на кусок.
func (c *Collection) goldSpan(cs EvalCase) (goldRef, bool) {
	doc, ord, ok := splitChunkID(cs.ChunkID)
	if !ok {
		return goldRef{}, false
	}
	g := goldRef{doc: doc, ord: ord}
	if info, ok := c.ChunkByRef(uint32(doc), uint32(ord)); ok {
		g.from, g.to = info.UnitFrom, info.UnitTo
	}
	return g, true
}

// splitChunkID разбирает ссылку «books/98#173» на номер книги и номер куска.
// Своего разбора тут не избежать: в наборе ссылка строкой, а сравнивать надо
// по книге и порядковому номеру.
func splitChunkID(id string) (doc, ord int, ok bool) {
	slash := strings.LastIndexByte(id, '/')
	hash := strings.LastIndexByte(id, '#')
	if slash < 0 || hash < slash {
		return 0, 0, false
	}
	d, err1 := strconv.Atoi(id[slash+1 : hash])
	o, err2 := strconv.Atoi(id[hash+1:])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return d, o, true
}

func abstainTableBy(points []GapPoint, thresholds []float64, by func(GapPoint) float64) []AbstainRow {
	rows := make([]AbstainRow, 0, len(thresholds))
	for _, t := range thresholds {
		r := AbstainRow{Threshold: t}
		for _, p := range points {
			if by(p) >= t {
				continue
			}
			r.Silent++
			if p.Hit {
				r.SilentWrong++
			} else {
				r.SilentRight++
			}
		}
		rows = append(rows, r)
	}
	return rows
}

// TopGap — разрыв первого и второго места выдачи: (score₁ − score₂) / score₁.
// Одна формула на замер и на работу (find.SignalsOf), иначе порог, снятый
// замером, будет применён к другому числу.
func TopGap(hits []Result) float64 {
	if len(hits) < 2 || hits[0].Score <= 0 {
		return 0
	}
	return (hits[0].Score - hits[1].Score) / hits[0].Score
}

// EvalOpts — как мерить.
type EvalOpts struct {
	K         int // сколько кусков смотреть, 0 — десять
	MaxPerDoc int // тот же предел, что и в работе; 0 — три

	// SemanticWeight перекрывает вес смыслового списка при слиянии.
	// 0 — как в работе. Нужен подбору: см. kb_semantic_search.md.
	SemanticWeight float64

	// TableBoost перекрывает надбавку кускам-таблицам. 0 — как в работе.
	// Ручка меряется перебором на наборе терминов, а не выбирается на глаз.
	TableBoost float64

	// RRFK перекрывает постоянную слияния списков. 0 — как в работе.
	RRFK float64

	// Rerank — вторая ступень. Пусто — мерим одну ступень, как раньше.
	//
	// Подбор второй ступени — это две ручки сразу: сколько кандидатов ей дать
	// и что именно подавать, кусок целиком или выдержку. Обе меряются тем же
	// набором и тем же прогоном, что и всё остальное; гадать про них незачем.
	Rerank     Reranker
	RerankOpts RerankOpts

	// KeepAdjacent — не выбрасывать соседние куски (SearchOpts.KeepAdjacent):
	// замер отделяет «поиск не нашёл» от «нашёл, но выбросила наша доводка».
	KeepAdjacent bool

	// NearOrds — какое расстояние в номерах кусков считать «рядом» при разборе
	// промахов. 0 — умолчание 2: доводка выдачи выбрасывает соседние куски
	// (±1), и брать надо чуть шире, чтобы увидеть именно их.
	NearOrds int

	// WideK — щедрый бюджет для второго прогона по промахнувшимся вопросам:
	// сколько кусков смотреть, когда предел на книгу снят вовсе. 0 — не мерить.
	//
	// **Зачем.** Замер отвечал двумя исходами: нашлось в первых K или «мимо».
	// Между ними спрятан третий, самый обидный: нужный кусок **нашёлся, но
	// не поместился** — его срезал `top_k` или предел на книгу (`max_per_book`).
	// Лечится он не переранжированием и не переписыванием запроса, а бюджетом
	// выдачи, то есть совсем другой ручкой («LLM Engineering with Python», 2026,
	// разд. 25: «It ranked well but appears in dropped | The dropped field |
	// Budget»; этап 105, Ж2). Пока исход не назван, он выглядел промахом
	// извлечения, и лечили его не тем.
	//
	// Прогон идёт ТЕМ ЖЕ путём и с той же второй ступенью — меняются только
	// два предела. Поэтому «достижимо щедрым бюджетом» значит именно то, что
	// написано: работа теряет этот кусок на бюджете, а не на поиске.
	WideK int

	// Search — чем искать. nil — коллекция сама (SearchWith плюс Rerank).
	// Замер через тот же путь, что и работа (find.Books), задаётся отсюда:
	// пакет kb не может звать find, а мерить надо ровно то, чем ищут (этап 91, R2.9).
	Search func(ctx context.Context, query string, opt SearchOpts, want int) ([]Result, error)
}

func (o EvalOpts) norm() EvalOpts {
	if o.K <= 0 {
		o.K = 10
	}
	if o.MaxPerDoc <= 0 {
		o.MaxPerDoc = 3
	}
	if o.NearOrds <= 0 {
		o.NearOrds = 2
	}
	return o
}

// searchOptsFor собирает настройки поиска под режим замера.
//
// Отдельная тонкость — режим «только смыслы». Выключить словесный поиск нечем:
// он и есть основной, а смысловой к нему добавляется. Поэтому вес словесного
// списка убирается в ноль через SemanticOnly, и слияние остаётся с одним
// списком — ровно так же, как оно ведёт себя, когда векторов ещё нет.
func searchOptsFor(mode EvalMode, o EvalOpts) SearchOpts {
	opt := SearchOpts{TopK: o.K, MaxPerDoc: o.MaxPerDoc, TableBoost: o.TableBoost,
		SemanticWeight: o.SemanticWeight, RRFK: o.RRFK, KeepAdjacent: o.KeepAdjacent}
	switch mode {
	case EvalLexical:
		opt.Semantic = false
	case EvalSemantic:
		opt.Semantic = true
		opt.SemanticOnly = true
	default:
		opt.Semantic = true
	}
	return opt
}

// Eval прогоняет набор вопросов и считает метрики.
//
// emb нужен режимам со смыслами; без него они возвращают отчёт с нулями
// и все вопросы в Missed — это честнее, чем молча мерить один словесный поиск.
func (c *Collection) Eval(ctx context.Context, cases []EvalCase, mode EvalMode,
	o EvalOpts, emb Embedder) (EvalReport, error) {

	o = o.norm()
	rep := EvalReport{Mode: mode, Cases: len(cases)}
	if len(cases) == 0 {
		return rep, nil
	}

	opt := searchOptsFor(mode, o)
	var ranks, ranksNear []int
	for _, cs := range cases {
		searchOpt := opt
		if o.Rerank != nil {
			// Первая ступень обязана отдать столько, сколько будет
			// переранжировано: иначе второй ступени нечего переставлять.
			if n := o.RerankOpts.Norm().Candidates; n > searchOpt.TopK {
				searchOpt.TopK = n
			}
		}
		var res []Result
		var err error
		if o.Search != nil {
			res, err = o.Search(ctx, cs.Query, opt, opt.TopK)
		} else {
			res, err = c.SearchWith(ctx, cs.Query, searchOpt, emb)
			if err == nil && o.Rerank != nil {
				res, err = Rerank(ctx, o.Rerank, cs.Query, res, opt.TopK, o.RerankOpts)
			}
		}
		if err != nil {
			return rep, err
		}
		rank := 0
		for i, r := range res {
			if cs.hit(r) {
				rank = i + 1
				break
			}
		}
		// Мера «та же страница»: место первого куска, который либо эталон,
		// либо его сосед по той же странице (см. RecallNear).
		rankNear := rank
		if gold, ok := c.goldSpan(cs); ok {
			for i, r := range res {
				if rankNear > 0 && i+1 >= rankNear {
					break
				}
				if gold.near(r, o.NearOrds) {
					rankNear = i + 1
					break
				}
			}
		}
		if rankNear > 0 {
			ranksNear = append(ranksNear, rankNear)
			rep.RecallNear++
			rep.MRRNear += 1 / float64(rankNear)
			rep.NDCGNear += 1 / math.Log2(float64(rankNear)+1)
		}
		gp := GapPoint{Gap: TopGap(res), Hit: rank > 0}
		if len(res) > 0 {
			gp.Top1 = res[0].Score
		}
		rep.Gaps = append(rep.Gaps, gp)
		if rank == 0 {
			rep.Missed = append(rep.Missed, cs.Query)
			// Промах прибора или промах поиска: была ли в выдаче та же книга
			// и не стоял ли рядом с эталоном другой её кусок.
			if doc, ord, ok := splitChunkID(cs.ChunkID); ok {
				best := -1
				for _, r := range res {
					d, o, ok := splitChunkID(r.ID)
					if !ok || d != doc {
						continue
					}
					dist := o - ord
					if dist < 0 {
						dist = -dist
					}
					if best < 0 || dist < best {
						best = dist
					}
				}
				if best >= 0 {
					rep.NearBook++
					rep.NearDist = append(rep.NearDist, best)
					if best <= o.NearOrds {
						rep.NearChunk++
					}
				}
			}
			// Третий исход: кусок достижим, но не поместился. Спрашиваем только
			// у промахнувшихся вопросов — у найденных спрашивать нечего.
			if o.WideK > 0 {
				wide := opt
				wide.TopK = o.WideK
				// Предел на книгу снят вовсе. Именно отрицательное, не ноль:
				// путь работы (find.Books) читает ноль как «умолчание
				// коллекции», и первый замер Ж2 (24.09.2026) с нулём мерил
				// щедрый бюджет при прежнем пределе на книгу.
				wide.MaxPerDoc = -1
				var wres []Result
				var werr error
				if o.Search != nil {
					wres, werr = o.Search(ctx, cs.Query, wide, o.WideK)
				} else {
					wres, werr = c.SearchWith(ctx, cs.Query, wide, emb)
					if werr == nil && o.Rerank != nil {
						wres, werr = Rerank(ctx, o.Rerank, cs.Query, wres, o.WideK, o.RerankOpts)
					}
				}
				if werr != nil {
					return rep, werr
				}
				for i, r := range wres {
					if cs.hit(r) {
						rep.Dropped = append(rep.Dropped, cs.Query)
						if i+1 <= opt.TopK {
							rep.DroppedInK++
						}
						break
					}
				}
			}
			continue
		}
		ranks = append(ranks, rank)
		rep.Recall++
		rep.MRR += 1 / float64(rank)
		// Правильный ответ один, поэтому идеальный DCG равен единице
		// и nDCG сводится к 1/log2(место+1).
		rep.NDCG += 1 / math.Log2(float64(rank)+1)
	}

	n := float64(len(cases))
	rep.Recall /= n
	rep.MRR /= n
	rep.NDCG /= n
	rep.RecallNear /= n
	rep.MRRNear /= n
	rep.NDCGNear /= n
	if len(ranksNear) > 0 {
		sum := 0
		for _, r := range ranksNear {
			sum += r
		}
		rep.AvgRankNear = float64(sum) / float64(len(ranksNear))
	}
	if len(ranks) > 0 {
		sort.Ints(ranks)
		sum := 0
		for _, r := range ranks {
			sum += r
		}
		rep.AvgRank = float64(sum) / float64(len(ranks))
	}
	return rep, nil
}

// containsFold — вхождение без учёта регистра. Названия книг приходят
// из метаданных и из имени файла, и регистр у них разный.
func containsFold(hay, needle string) bool {
	return len(needle) > 0 && indexFold(hay, needle) >= 0
}

func indexFold(hay, needle string) int {
	h, n := []rune(lowerASCIICyr(hay)), []rune(lowerASCIICyr(needle))
	if len(n) == 0 || len(n) > len(h) {
		return -1
	}
	for i := 0; i+len(n) <= len(h); i++ {
		ok := true
		for j := range n {
			if h[i+j] != n[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func lowerASCIICyr(s string) string {
	out := []rune(s)
	for i, r := range out {
		switch {
		case r >= 'A' && r <= 'Z':
			out[i] = r + 32
		case r >= 'А' && r <= 'Я':
			out[i] = r + 32
		case r == 'Ё':
			out[i] = 'ё'
		}
	}
	return string(out)
}
