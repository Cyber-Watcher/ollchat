// Поиск по графу и книгам без обращения к модели.
//
// **Зачем отдельный пакет.** Найти что-то в библиотеке до сих пор можно было,
// только спросив модель: она позовёт kb_search, подумает и перескажет. Это
// секунды ожидания и расход контекста там, где человеку нужно просто увидеть
// подходящие куски книг со ссылками.
//
// Кирпичи были, но собраны не для человека: `/kb search` не показывает связей
// из графа, а `mixer.Build` собирает **промпт для модели** — с привратником,
// с требованием включённого подмешивания и с обвязкой «опора для ответа,
// а не сам ответ». Для явной команды поиска всё это лишнее: человек уже сказал,
// чего хочет.
//
// **Почему не в internal/ui.** Тем же ядром пользуются `/search`, `/graph find`
// и, когда понадобится, ключ командной строки и служба MCP. Две копии одной
// политики расходятся в первую же правку — этот урок уже оплачен подмешиванием.
package find

import (
	"context"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Deps — что нужно поиску. Всё передаётся явно: пакет ничего не открывает
// и не закрывает сам, потому что временем жизни графа и коллекции распоряжается
// вызывающий (у интерфейса свой кэш, у службы — свой).
type Deps struct {
	// Steps — журнал шагов; nil — не писать.
	Steps *steplog.Writer
	// Coll — локальная коллекция: по ней читаются подтверждения графа
	// и считается ранжирование. Обязательна для входа в граф.
	Coll *kb.Collection
	// Source — где искать по книгам: локальная коллекция или сетевой драйвер.
	// nil — Coll. Так одним ядром ходят и инструменты, у которых библиотека
	// может быть удалённой (этап 91, R2.3).
	Source   kb.Source
	Graph    *graph.Graph // nil — графа нет, показываем одни выдержки
	Embedder kb.Embedder  // nil — смысловой поиск не настроен
	Reranker kb.Reranker  // nil — второй ступени нет
}

// Opts — числа отбора и оформления. Приходят из настроек и от ключей команды.
type Opts struct {
	// Turn — идентификатор обмена для журнала шагов.
	Turn string
	// Mode — кто ищет: "search" (/search), "tool" (kb_search), "mix"
	// (подмешивание), "serve" (служба), "eval" (замер), "kb" (/kb search).
	// Только для журнала шагов: поведение одно на всех.
	Mode string
	// ExpandLimit — сколько понятий графа дописывать к запросу для книг;
	// 0 — все найденные (так ищет /search), 3 — так ищут инструмент и подмес.
	ExpandLimit int
	// Docs — искать только в этих книгах (номера документов); пусто — во всех.
	Docs []uint32
	// Exact — требовать точного вхождения подстроки.
	Exact string
	// SemanticOnly — только смысловой список, без словесного (замер).
	SemanticOnly bool
	// TableBoost и RRFK — числа отбора базы знаний; 0 — умолчания пакета kb.
	TableBoost float64
	RRFK       float64
	Collection string // имя для ссылок вида «books/12#37»

	// Книги.
	TopK           int
	MaxPerBook     int
	MinCosine      float64
	SemanticWeight float64
	Semantic       bool
	Rerank         bool // вторая ступень, если настроена

	// Граф.
	Entities  int                // сколько понятий брать; 0 — шесть
	Neighbors int                // сколько связей у каждого; 0 — пять
	Rank      graph.NeighborRank // ранжирование связей по вопросу

	// Оформление и сроки.
	Full         bool          // показывать куски целиком
	SnippetRunes int           // длина короткой выдержки; 0 — 320
	QueryTimeout time.Duration // сколько ждать вектор вопроса; 0 — не ждать
	RerankOpts   kb.RerankOpts
}

func (o Opts) norm() Opts {
	if o.Entities <= 0 {
		o.Entities = 6
	}
	if o.Neighbors <= 0 {
		o.Neighbors = 5
	}
	if o.SnippetRunes <= 0 {
		o.SnippetRunes = 320
	}
	return o
}

// Excerpt — одна выдержка: то, что видит человек и что читает /read.
type Excerpt struct {
	ID       string // «books/12#37»
	Book     string
	Path     string // путь к файлу книги: по нему Ctrl+O открывает её целиком
	Author   string
	Year     int
	Unit     string // «стр.» или «разд.»
	From, To int
	Text     string // кусок целиком
	Snippet  string // короткая выдержка
	Code     bool
	Graph    bool // пришёл подтверждением графа, а не поиском по книгам
}

// Result — что нашлось.
type Result struct {
	Query      string
	Collection string

	Entities  []graph.FoundEntity
	Relations []graph.FoundRelation
	Excerpts  []Excerpt

	// WordsOnly и WordsWhy — смысловой поиск не участвовал и почему именно.
	// Разделено на признак и объяснение, потому что причины разные: сервер
	// не ответил (поломка) и модель не задана (так настроено).
	WordsOnly bool
	WordsWhy  string

	// GraphNote — оговорка о графе: его нет, он собран не весь, понятия
	// не нашлись. Пусто — оговорок нет.
	GraphNote string

	// Hits — выдача по книгам как есть, до слияния с подтверждениями графа:
	// её читают те, кто печатает для модели или отдаёт по сети.
	Hits []kb.Result
	// Note — объяснение коллекции о последнем поиске (смысл не отработал и т. п.).
	Note string
	// Signals — признаки того, нашлось ли вообще (этап 91, R2.11).
	Signals Signals
}

// Signals — признаки качества выдачи, по которым решают, есть ли ответ
// в книгах вовсе. Порог не назначен: он снимается замером на 457 вопросах
// (RAGFindingsFromBooks.md, §3), а до него признаки только пишутся
// в журнал шагов.
type Signals struct {
	Hits    int     // сколько кусков вернул поиск
	Top1Gap float64 // (score₁ − score₂) / score₁ — разрыв первого и второго места
	Top1    float64 // оценка первого места как есть; сопоставима между запросами только у реранкера
}

// signalsOf считает признаки по выдаче.
func signalsOf(hits []kb.Result) Signals {
	s := Signals{Hits: len(hits), Top1Gap: kb.TopGap(hits)}
	if len(hits) > 0 {
		s.Top1 = hits[0].Score
	}
	return s
}

// AbstainScoreNote — предупреждение по абсолютной оценке первого места. Только
// для выдачи реранкера: его шкала сопоставима между запросами (по делу около +1,
// не по делу около −11), а оценки слияния по рангам — нет. Замер 04.09.2026
// на 457 вопросах (этап 89, шаг 4): порог −2 молчит на 12 вопросах, из них 11 —
// где нужного куска и не было, 1 — где был (0.6% найденного); разрыв мест такой
// точности не даёт ни при каком пороге. minScore nil — порога нет.
func AbstainScoreNote(s Signals, minScore *float64, reranked bool) string {
	if minScore == nil || !reranked || s.Hits == 0 || s.Top1 >= *minScore {
		return ""
	}
	return fmt.Sprintf("ВНИМАНИЕ: лучший найденный кусок оценён реранкером на %.2f при пороге %.2f — "+
		"по замеру это почти всегда значит, что в книгах об этом нет. Не выдавай куски ниже "+
		"за ответ; скажи, что в библиотеке этого не нашлось, если они не по вопросу.", s.Top1, *minScore)
}

// SignalsOf — те же признаки для того, кто ищет через Books, а не Search:
// инструмент kb_search решает по ним, честно ли сказать «в книгах нет».
func SignalsOf(hits []kb.Result) Signals { return signalsOf(hits) }

// AbstainNote — предупреждение о слабой выдаче, если разрыв первого и второго
// места ниже порога gap; пусто — выдача уверенная или порога нет.
//
// **Почему не молчать вовсе.** Куски всё же отдаются: порог снят замером
// на наборе, где «мимо» значит «нужного куска нет в первых K», а не «в книгах
// пусто», и на границе он ошибается. Предупреждение сверху даёт модели
// повод сказать «в книгах об этом нет» вместо ссылки на случайную страницу —
// ровно то, чего требует Corrective RAG (RAGFindingsFromBooks.md, §3).
func AbstainNote(s Signals, gap float64) string {
	if gap <= 0 || s.Hits < 2 || s.Top1Gap >= gap {
		return ""
	}
	return fmt.Sprintf("ВНИМАНИЕ: выдача неуверенная — разрыв первого и второго места %.3f при пороге %.2f. "+
		"Скорее всего, в книгах об этом нет: не выдавай куски ниже за ответ, "+
		"а скажи, что в библиотеке этого не нашлось, если они не по вопросу.", s.Top1Gap, gap)
}

// source — где искать по книгам.
func (d Deps) source() kb.Source {
	if d.Source != nil {
		return d.Source
	}
	if d.Coll != nil {
		return d.Coll
	}
	return nil // а не типизированный nil: с ним проверка на nil молчит
}

// Expand дополняет запрос именами понятий графа и их синонимами — перевод
// вопроса на язык библиотеки. Один предел на всех: /search дописывает все
// найденные понятия (0), инструмент и подмес — три (этап 91, R2.10).
func Expand(query string, ents []graph.FoundEntity, o Opts) string {
	return graph.ExpandQuery(query, ents, o.ExpandLimit)
}

// Empty сообщает, что показывать нечего.
func (r Result) Empty() bool {
	return len(r.Entities) == 0 && len(r.Excerpts) == 0
}

// Search ищет по графу и книгам.
//
// Ошибка возвращается только тогда, когда не удалось вообще ничего. Отсутствие
// графа, недоступный эмбеддер и пустая выдача графа ошибками не считаются:
// поиск обязан отдать то, что смог, и объяснить, чего не смог.
func Search(ctx context.Context, d Deps, query string, o Opts) (Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Result{}, fmt.Errorf("пустой запрос")
	}
	if d.source() == nil {
		return Result{}, fmt.Errorf("коллекция не выбрана")
	}
	o = o.norm()
	res := Result{Query: query, Collection: o.Collection}
	started := time.Now()
	var msGraph, msBooks int64
	defer func() {
		ids := make([]string, 0, len(res.Excerpts))
		for _, e := range res.Excerpts {
			ids = append(ids, e.ID)
		}
		d.Steps.Write(steplog.Step{Turn: o.Turn, Kind: steplog.KindSearch, Args: query,
			MS: time.Since(started).Milliseconds(),
			Extra: map[string]any{"mode": o.Mode, "ms_graph": msGraph, "ms_books": msBooks,
				"entities": len(res.Entities), "excerpts": len(res.Excerpts), "ids": ids,
				"hits": res.Signals.Hits, "top1_gap": res.Signals.Top1Gap,
				"words_only": res.WordsOnly, "collection": o.Collection}})
	}()

	// Вектор вопроса считается ОДИН раз на оба поиска.
	//
	// И если он не посчитался, дальше в поиск по книгам уходит nil, а не
	// эмбеддер: иначе человек ждёт срок дважды на том же мёртвом сервере.
	// Замер 29.08.2026: пока карту занимает сборка графа, сервер не отдаёт
	// эмбеддинги вовсе, и это состояние держится часами.
	qv, emb, why := QueryVector(ctx, d, query, o)
	res.WordsOnly, res.WordsWhy = why != "", why

	if d.Graph != nil && d.Coll != nil {
		tGraph := time.Now()
		gres := d.Graph.Search(query, graph.SearchOpts{
			TopEntities:  o.Entities,
			TopNeighbors: o.Neighbors,
			TopChunks:    o.TopK,
			// MinMentions нулевой, в отличие от подмешивания. Там отсечка
			// бережёт контекст модели от одноразовых понятий, здесь человек
			// ищет как раз редкое — и прятать это от него незачем.
			MinMentions: 0,
			Rank:        graph.RankWith(d.Coll),
			QueryVector: qv,
			Neighbors:   o.Rank,
		})
		msGraph = time.Since(tGraph).Milliseconds()
		res.Entities, res.Relations, res.GraphNote = gres.Entities, gres.Relations, gres.Note
		res.Excerpts = fromGraph(d.Coll, gres.Chunks, o)

		// Граф собран не по всей библиотеке — об этом надо сказать прямо,
		// иначе «ничего не нашлось» прочтётся как «в книгах об этом не пишут».
		//
		// Оговорка жила только в командной строке, а `/search` в интерфейсе
		// молчал — то есть человек, который ищет чаще всего, её и не видел.
		if st := d.Graph.Stats(d.Coll.ChunkCount()); st.Pending > 0 {
			percent := 100 * st.Covered / max(d.Coll.ChunkCount(), 1)
			res.GraphNote = strings.TrimSpace(res.GraphNote + fmt.Sprintf(
				"\nграф собран по %d%% библиотеки (%d фрагментов из %d)",
				percent, st.Covered, d.Coll.ChunkCount()))
		}
	} else {
		res.GraphNote = "граф коллекции не собран — показаны только выдержки из книг"
	}

	// По книгам ищем вопросом, дополненным именами понятий графа.
	//
	// Замер 30.08.2026: `/search горутина` отдавал одни русские книги, хотя
	// векторы посчитаны для всей коллекции и язык они переходят — та же русская
	// фраза с ограничением по английской книге находила ровно нужные страницы.
	// Дело в слиянии: русский кусок попадает в оба списка сразу и получает
	// награду за согласие, английский — только в смысловой. Имя `goroutine`
	// из графа даёт английскому куску тот же второй голос (graph.ExpandQuery).
	bookQuery := Expand(query, res.Entities, o)

	tBooks := time.Now()
	hits, note, err := d.books(ctx, bookQuery, query, emb, o)
	msBooks = time.Since(tBooks).Milliseconds()
	if err != nil {
		// Поиск по книгам — единственная обязательная часть: если и он не вышел,
		// показывать нечего.
		if len(res.Entities) == 0 {
			return res, err
		}
		res.GraphNote = strings.TrimSpace(res.GraphNote + "\nпоиск по книгам не удался: " + err.Error())
		return res, nil
	}
	res.Note = note
	if res.WordsWhy == "" {
		res.WordsWhy = note
		res.WordsOnly = strings.Contains(res.WordsWhy, "только по словам")
	}

	res.Hits = hits
	res.Signals = signalsOf(hits)
	res.Excerpts = merge(hits, res.Excerpts, o)
	return res, nil
}

// Books — поиск по книгам одним ядром для всех входов: инструмента kb_search,
// подмешивания, службы, замера и /kb search (этап 91, R2). search — что искать
// (запрос, возможно дополненный именами понятий), question — вопрос человека
// для второй ступени. Возвращает выдачу и объяснение коллекции.
func Books(ctx context.Context, d Deps, search, question string, o Opts) ([]kb.Result, string, error) {
	if d.source() == nil {
		return nil, "", fmt.Errorf("коллекция не выбрана")
	}
	o = o.norm()
	_, emb, why := QueryVector(ctx, d, question, o)
	hits, note, err := d.books(ctx, search, question, emb, o)
	if note == "" {
		note = why
	}
	return hits, note, err
}

// books ищет по книгам. Запрос для поиска и вопрос для переранжирования
// разные: искать надо дополненным (имена понятий дают английским кускам второй
// голос), а кросс-энкодеру нужен вопрос человека — приписанные синонимы для
// него шум.
func (d Deps) books(ctx context.Context, search, question string, emb kb.Embedder, o Opts) ([]kb.Result, string, error) {
	opt := kb.DefaultSearchOpts()
	if o.TopK > 0 {
		opt.TopK = o.TopK
	}
	if o.MaxPerBook > 0 {
		opt.MaxPerDoc = o.MaxPerBook
	}
	opt.Semantic = o.Semantic
	opt.SemanticOnly = o.SemanticOnly
	opt.TableBoost = o.TableBoost
	opt.RRFK = o.RRFK
	opt.MinCosine = o.MinCosine
	opt.SemanticWeight = o.SemanticWeight
	opt.QueryTimeout = o.QueryTimeout
	opt.Docs = o.Docs
	opt.Exact = o.Exact

	// Вторая ступень читает вопрос вместе с куском и переставляет верхушку.
	// Ей нужны кандидаты сверх того, что показываем: первая ступень отдаёт
	// шире, реранкер отбирает. До этапа 91 (R2.4) так делали инструмент
	// и подмес, а /search переранжировал ровно TopK — одно ядро, одно правило.
	want := opt.TopK
	rerank := o.Rerank && d.Reranker != nil
	if rerank {
		// Norm: ноль в настройках значит «двадцать» (kb.rerank_candidates),
		// и ширина первой ступени обязана читать его так же, как сама вторая.
		// Без этого реранкер переставлял ровно TopK — замер 04.09.2026:
		// recall с реранкером падал до recall одной ступени (0.370 → 0.350).
		if n := o.RerankOpts.Norm().Candidates; n > opt.TopK {
			opt.TopK = n
		}
	}

	src := d.source()
	hits, err := src.SearchWith(ctx, search, opt, emb)
	note := src.SearchNote()
	if err != nil || !rerank {
		if len(hits) > want {
			hits = hits[:want]
		}
		return hits, note, err
	}
	// Сбой службы не должен лишать человека выдачи.
	ranked, rerr := kb.Rerank(ctx, d.Reranker, question, hits, want, o.RerankOpts)
	if rerr != nil {
		if len(hits) > want {
			hits = hits[:want]
		}
		return hits, note, nil
	}
	return ranked, note, nil
}

// queryVector считает вектор вопроса и решает, идти ли дальше со смыслом.
//
// Возвращает вектор для графа, эмбеддер для поиска по книгам (nil, если смысл
// недоступен) и объяснение — пустое, когда всё в порядке.
// QueryVector считает вектор вопроса и решает, идти ли дальше со смыслом.
// Одно место на /search, инструменты и команды графа (этап 91, R2.5):
// до того сверка модели эмбеддера с векторами графа жила в трёх копиях.
func QueryVector(ctx context.Context, d Deps, query string, o Opts) ([]int8, kb.Embedder, string) {
	if !o.Semantic {
		return nil, nil, ""
	}
	if d.Embedder == nil {
		return nil, nil, "смысловой поиск не настроен (kb.embed_model) — искал по словам"
	}
	if o.QueryTimeout <= 0 {
		return nil, nil, "ожидание вектора выключено (kb.query_timeout = 0) — искал по словам"
	}

	qctx, cancel := context.WithTimeout(ctx, o.QueryTimeout)
	defer cancel()
	vecs, err := d.Embedder.Embed(qctx, []string{query})
	if err != nil || len(vecs) == 0 {
		// Срок назван прямо. Ошибка клиента на исходе срока звучит как
		// «генерация прервана», и человек ищет обрыв там, где его нет:
		// на деле карта занята сборкой графа и эмбеддинги не считаются вовсе.
		if qctx.Err() != nil {
			return nil, nil, fmt.Sprintf("смысловой поиск недоступен: %s не ответил за %s"+
				" (kb.query_timeout) — искал по словам", d.Embedder.Model(), o.QueryTimeout)
		}
		return nil, nil, fmt.Sprintf("смысловой поиск недоступен (%v) — искал по словам", err)
	}

	// Вектор графа берётся только когда он сопоставим: посчитан той же моделью
	// и той же размерности. Иначе расстояния бессмысленны, а поиск — случаен.
	var qv []int8
	if d.Graph != nil {
		if info := d.Graph.VectorsInfo(); info.Ready &&
			info.Model == d.Embedder.Model() && len(vecs[0]) == info.Dim {
			qv = kb.Quantize(vecs[0])
		}
	}
	return qv, d.Embedder, ""
}

// fromGraph превращает подтверждения графа в выдержки.
func fromGraph(src *kb.Collection, keys []graph.ChunkKey, o Opts) []Excerpt {
	out := make([]Excerpt, 0, len(keys))
	for _, k := range keys {
		info, ok := src.ChunkByRef(k.Doc, k.Ord)
		if !ok {
			continue
		}
		out = append(out, Excerpt{
			ID:   fmt.Sprintf("%s/%d#%d", o.Collection, k.Doc, k.Ord),
			Book: info.Book.Title, Path: info.Book.Path, Author: info.Book.Author, Year: info.Book.Year,
			Unit: info.Unit, From: info.UnitFrom, To: info.UnitTo,
			Text: info.Text, Snippet: cut(info.Text, o.SnippetRunes), Graph: true,
		})
	}
	return out
}

// merge складывает выдержки в один список без повторов.
//
// Сперва найденное поиском по книгам — оно отобрано по вопросу целиком, —
// затем подтверждения графа, которых там ещё нет. Два отдельных списка цитат
// в терминале читаются как повтор одного и того же.
func merge(hits []kb.Result, fromG []Excerpt, o Opts) []Excerpt {
	out := make([]Excerpt, 0, len(hits)+len(fromG))
	seen := make(map[string]bool, len(hits)+len(fromG))

	for _, h := range hits {
		if seen[h.ID] {
			continue
		}
		seen[h.ID] = true
		snippet := h.Snippet
		if snippet == "" {
			snippet = cut(h.Text, o.SnippetRunes)
		}
		out = append(out, Excerpt{
			ID: h.ID, Book: h.Book, Path: h.Path, Author: h.Author, Year: h.Year,
			Unit: h.Unit, From: h.UnitFrom, To: h.UnitTo,
			Text: h.Text, Snippet: snippet, Code: h.Code,
		})
	}
	for _, e := range fromG {
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	if o.TopK > 0 && len(out) > o.TopK {
		out = out[:o.TopK]
	}
	return out
}

// cut обрезает текст до n знаков по границе слова.
func cut(s string, n int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	head := string(r[:n])
	if i := strings.LastIndexByte(head, ' '); i > n/2 {
		head = head[:i]
	}
	return head + "…"
}
