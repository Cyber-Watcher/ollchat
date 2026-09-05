package kb

import (
	"context"
	"math"
	"sort"
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
	return o
}

// searchOptsFor собирает настройки поиска под режим замера.
//
// Отдельная тонкость — режим «только смыслы». Выключить словесный поиск нечем:
// он и есть основной, а смысловой к нему добавляется. Поэтому вес словесного
// списка убирается в ноль через SemanticOnly, и слияние остаётся с одним
// списком — ровно так же, как оно ведёт себя, когда векторов ещё нет.
func searchOptsFor(mode EvalMode, o EvalOpts) SearchOpts {
	opt := SearchOpts{TopK: o.K, MaxPerDoc: o.MaxPerDoc, TableBoost: o.TableBoost, SemanticWeight: o.SemanticWeight, RRFK: o.RRFK}
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
	var ranks []int
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
		gp := GapPoint{Gap: TopGap(res), Hit: rank > 0}
		if len(res) > 0 {
			gp.Top1 = res[0].Score
		}
		rep.Gaps = append(rep.Gaps, gp)
		if rank == 0 {
			rep.Missed = append(rep.Missed, cs.Query)
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
