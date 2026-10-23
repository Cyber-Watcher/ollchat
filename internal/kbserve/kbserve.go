package kbserve

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Сервер знаний: общая библиотека организации.
//
// **Один пакет на две программы.** Службу поднимают двумя способами:
// `ollchat --serve` — тем же бинарём, которым администратор индексирует
// и строит граф, и `ollmcp --http` — вместе с входом MCP для сторонних
// клиентов. Реализация одна: две копии серверного кода разошлись бы
// в первой же правке, а расхождение здесь означало бы, что у сотрудников
// поиск работает по-разному в зависимости от того, чем подняли службу.
//
// **Вход данных — отдельно от входа MCP.**
//
// **Зачем второй вход, если MCP уже есть.** Инструменты MCP отдают модели
// **готовый текст**: «[1] Книга · 2024 г. · стр. 128 …». Полей в нём нет,
// и собрать из такого ответа `kb.Result` обратно нельзя — разбирать
// собственный формат вывода значит завести вторую точку правды и однажды
// разойтись с ней.
//
// Поэтому: MCP остаётся для моделей, а `/api/v1/…` отдаёт те же данные полями.
// Это ровно то же знание, из той же коллекции, тем же движком — разной
// остаётся только упаковка.
//
// **Вектор вопроса считает сервер, а не клиент.** Так вышло проще и правильнее:
// в организации эмбеддер настроен один раз на сервере, клиенту не нужно ни
// адреса Ollama, ни имени модели, и рассогласоваться моделям негде. Клиент
// шлёт текст.
//
// **Поколение индекса.** Локально свежесть определяется отпечатком каталога
// (`graph.Cache.dirStamp`), но по сети каталога не видно. Поэтому каждый ответ
// несёт номер поколения: администратор долил книги — номер сменился, и клиент
// знает, что показанное устарело. Без этого повторилась бы беда, ради которой
// заведён `internal/kb/reopen.go`: служба неделями отдавала вчерашний текст.

// SearchRequest — запрос поиска по книгам.
type SearchRequest struct {
	Collection string `json:"collection"`
	Query      string `json:"query"`

	// Числа отбора. Ноль означает «как настроено на сервере», а не «ноль»:
	// клиент вправе не иметь мнения, и тогда действует политика организации.
	TopK           int      `json:"top_k,omitempty"`
	MaxPerBook     int      `json:"max_per_book,omitempty"`
	MinCosine      float64  `json:"min_cosine,omitempty"`
	SemanticWeight float64  `json:"semantic_weight,omitempty"`
	Books          []uint32 `json:"books,omitempty"`
	Exact          string   `json:"exact,omitempty"`
}

// SearchResponse — выдача плюс то, что нужно клиенту для честного показа.
type SearchResponse struct {
	Results    []kb.Result `json:"results"`
	Note       string      `json:"note,omitempty"`
	Generation string      `json:"generation"`
	Collection string      `json:"collection"`
	// QueuedMS — сколько запрос ждал места в очереди тяжёлых запросов, мс;
	// нет поля — не ждал. Клиент говорит об этом строкой в выводе (решение
	// владельца 23.10.2026). Есть у всех ответов тяжёлых входов.
	QueuedMS int64 `json:"queued_ms,omitempty"`
}

// BusyResponse — тело ответа 503, когда места в очереди тяжёлых запросов за
// предел ожидания не нашлось. Busy — признак «служба занята, но жива»: по нему
// клиент отличает очередь от недоступности и на свои файлы не переходит.
type BusyResponse struct {
	Error    string `json:"error"`
	Busy     bool   `json:"busy"`
	QueuedMS int64  `json:"queued_ms"`
}

// QueueWaitHeader — заголовок, которым клиент говорит, сколько он готов ждать
// места в очереди, в миллисекундах. Служба ждёт меньшее из этого и своего
// предела (Opts.QueueWait) и отвечает 503 «занята» раньше, чем у клиента
// истечёт срок: иначе очередь выглядела бы для него недоступностью службы.
const QueueWaitHeader = "X-Ollchat-Queue-Wait-Ms"

// FindRequest — поиск по графу и книгам одной выдачей: то, что показывает
// команда /search (find.Search) и /kb search (без графа). Числа отбора —
// живые числа клиента; ноль — как настроено у службы.
type FindRequest struct {
	Collection     string  `json:"collection"`
	Query          string  `json:"query"`
	TopK           int     `json:"top_k,omitempty"`
	MaxPerBook     int     `json:"max_per_book,omitempty"`
	MinCosine      float64 `json:"min_cosine,omitempty"`
	SemanticWeight float64 `json:"semantic_weight,omitempty"`
	Entities       int     `json:"entities,omitempty"`
	Neighbors      int     `json:"neighbors,omitempty"`
	// Graph — входить ли в граф: /search — да, /kb search — нет.
	Graph bool `json:"graph,omitempty"`
	// Rerank — вторая ступень отбора (ключ -r), если она настроена у службы.
	Rerank bool `json:"rerank,omitempty"`
	// Full — куски целиком, а не выдержками (ключ -f).
	Full bool `json:"full,omitempty"`
}

// FindResponse — выдача /search службы: разобранная (Result — для /read N
// и списка Ctrl+F у клиента) и напечатанная (Text — find.Render по файлам
// службы: выдержки под связями и годы связей клиенту взять неоткуда).
type FindResponse struct {
	Result     find.Result `json:"result"`
	Text       string      `json:"text"`
	Note       string      `json:"note,omitempty"`
	Generation string      `json:"generation"`
	Collection string      `json:"collection"`
	QueuedMS   int64       `json:"queued_ms,omitempty"`
}

// AroundRequest — запрос куска целиком с соседями.
type AroundRequest struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Around     int    `json:"around,omitempty"`
}

// CollectionInfo — что клиент должен знать о коллекции до первого вопроса.
type CollectionInfo struct {
	Name       string `json:"name"`
	Books      int    `json:"books"`
	Chunks     int    `json:"chunks"`
	Vectors    int    `json:"vectors"`
	VecModel   string `json:"vec_model,omitempty"`
	VecDim     int    `json:"vec_dim,omitempty"`
	Generation string `json:"generation"`
}

// generation — признак того, что содержимое коллекции изменилось.
//
// Считается по составу, а не по времени: время правки сбивается копированием
// и переносом, а число книг, кусков и векторов меняется ровно тогда, когда
// меняется само знание. Долили книгу — сменилось; посчитали смыслы —
// сменилось; уплотнили — сменилось.
//
// Это **признак различия, а не версия**: сравнивать его на «больше-меньше»
// нельзя, только на равенство.
func Generation(st kb.Stats) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d|%d|%d|%d|%s|%s",
		st.Indexed, st.Chunks, st.Vectors, st.Segments, st.VecModel, st.Analyzer)
	return fmt.Sprintf("%016x", h.Sum64())
}

// Opts — из чего складывается служба.
type Opts struct {
	Base    *kb.Base    // библиотека на диске: её собрал администратор
	Emb     kb.Embedder // чем считать вектор вопроса — настроен здесь, не у клиента
	Default string      // коллекция по умолчанию
	Token   string      // ключ доступа; пусто — проверки нет

	// TableBoost — надбавка кускам-таблицам (kb.table_boost); 0 — умолчание.
	TableBoost float64
	// Reranker — вторая ступень отбора; nil — её нет. До этапа 91 (R2.8)
	// служба искала одной ступенью, тогда как /search и kb_search — двумя.
	Reranker kb.Reranker
	// RerankOpts — числа второй ступени.
	RerankOpts kb.RerankOpts
	// Translator — перевод вопроса для словесной ветки (kb.translate); nil — без.
	Translator find.Translator

	// Graph — как отвечать на вопросы к графу. Пусто — граф не раздаётся,
	// и клиент получит внятный отказ вместо молчания.
	Graph GraphServer

	// HeavySlots — сколько тяжёлых запросов (поиск, граф) идёт разом;
	// 0 — heavySlots.
	HeavySlots int

	// QueueWait — сколько запрос ждёт места среди HeavySlots, прежде чем
	// служба ответит 503 «занята»; 0 — queueWait, отрицательное — ждать, пока
	// ждёт клиент (как до 23.10.2026). Клиент может попросить меньше
	// заголовком QueueWaitHeader.
	QueueWait time.Duration

	Verbose bool
}

// Пределы входа данных (аудит 07.10.2026, 4.2).
//
// Каждый поиск — это вектор вопроса и вторая ступень на видеокарте, а запрос
// к графу на холодной службе — открытие графа на десятки секунд и гигабайт
// памяти. Ни числа отбора, ни число таких запросов разом служба прежде
// не ограничивала: top_k = 1e6 или сотня параллельных вопросов занимали
// карту и память машины владельца, сколько хотел клиент.
const (
	// maxTopK — потолок top_k и max_per_book. Клиенту со своей второй ступенью
	// кандидатов нужно больше, чем показывать (rerank_candidates, умолчание —
	// двадцать), но не тысячи. Предел на книгу выше top_k ничего не значит.
	maxTopK = 100

	// heavySlots — сколько тяжёлых запросов служба ведёт разом. Остальные
	// ждут своей очереди, пока ждёт их клиент: на одной карте больше
	// одновременных векторов и переранжирований не ускоряют, а копят очередь
	// в Ollama.
	heavySlots = 4

	// queueWait — предел ожидания места в очереди. Меньше умолчания
	// kb.server_timeout клиента (30 с): занятая служба успевает сказать
	// «занята» прежде, чем клиент сочтёт её недоступной и откроет свой граф.
	queueWait = 10 * time.Second
)

// queueLimit — сколько ждать места для запроса r: предел службы, а если
// клиент просит меньше (QueueWaitHeader) — его. 0 — без предела.
func queueLimit(own time.Duration, r *http.Request) time.Duration {
	limit := own
	switch {
	case own == 0:
		limit = queueWait
	case own < 0:
		limit = 0
	}
	if ms, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get(QueueWaitHeader)), 10, 64); err == nil && ms > 0 {
		if asked := time.Duration(ms) * time.Millisecond; limit == 0 || asked < limit {
			limit = asked
		}
	}
	return limit
}

// busyError отвечает 503 с признаком «занята»: места за предел ожидания
// не нашлось, но служба жива.
func busyError(w http.ResponseWriter, slots int, waited time.Duration) {
	ms := waited.Milliseconds()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(BusyResponse{
		Error: fmt.Sprintf("служба занята: все %d мест тяжёлых запросов заняты, ждал в очереди %d мс",
			slots, ms),
		Busy: true, QueuedMS: ms,
	})
}

// searchOpts переводит запрос в числа поиска, приводя их к пределам службы.
//
// Ноль и отрицательное — «как настроено на сервере». Прежде отрицательный
// max_per_book снимал предел на книгу вовсе (так его читает find), а top_k
// не ограничивался ничем.
func searchOpts(req SearchRequest, name string, o Opts) find.Opts {
	return find.Opts{
		Mode: "serve", Collection: name, TableBoost: o.TableBoost,
		TopK: clampPositive(req.TopK, maxTopK), MaxPerBook: clampPositive(req.MaxPerBook, maxTopK),
		MinCosine: req.MinCosine, SemanticWeight: req.SemanticWeight,
		Docs: req.Books, Exact: req.Exact,
		Semantic: true, QueryTimeout: kb.DefaultQueryTimeout,
		Rerank: true, RerankOpts: o.RerankOpts,
	}
}

// clampPositive — v в пределах 1..max; ноль и меньше — ноль, то есть умолчание.
func clampPositive(v, max int) int {
	switch {
	case v <= 0:
		return 0
	case v > max:
		return max
	}
	return v
}

// GraphServer — то, что умеет отвечать на вопросы к графу.
//
// Интерфейс, а не конкретный тип: инструменты графа живут в `internal/tools`,
// и тянуть их сюда значило бы связать серверный пакет со всем реестром
// инструментов вместе с песочницей и правилами.
type GraphServer interface {
	// Tool выполняет именованный инструмент графа и отдаёт его текст —
	// тот самый, что увидела бы модель локально.
	Tool(ctx context.Context, collection, name string, args map[string]any) (string, error)

	// Search отдаёт разбираемый результат поиска по графу: понятия, связи,
	// ссылки на куски — и цепочку между двумя понятиями вопроса. Нужен
	// подмешиванию карты понятий к вопросу. Размер карты — из запроса
	// (req.Entities, req.Neighbors), ноль — по настройкам службы.
	Search(ctx context.Context, req GraphSearchRequest) (GraphSearchResponse, error)

	// Graph отдаёт граф коллекции и ранжирование соседей службы для поиска
	// по графу и книгам одной выдачей (вход /api/v1/find — команда /search
	// клиента). release отпускает граф. Ошибка — графа нет: выдача будет
	// из одних книг со строкой об этом, как у /search без графа.
	Graph(collection string) (g *graph.Graph, rank graph.NeighborRank, release func(), err error)
}

// Handler собирает маршрутизатор службы.
//
// Проверка ключа — одна на все входы: разных дверей с разными замками
// в одной службе быть не должно.
func Handler(o Opts) *http.ServeMux {
	mux := http.NewServeMux()
	Mount(mux, o)
	return mux
}

// Mount подключает вход данных к готовому маршрутизатору.
//
// Отдельно от Handler ради `ollmcp`: там на том же порту уже висит `/mcp`,
// и заводить второй маршрутизатор было бы незачем.
func Mount(mux *http.ServeMux, o Opts) {
	base, def, token, verbose := o.Base, o.Default, o.Token, o.Verbose
	if base == nil {
		return // библиотека не настроена — служба ничего не раздаёт
	}
	guard := func(fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !Auth(r, token) {
				http.Error(w, "нужен ключ доступа", http.StatusUnauthorized)
				if verbose {
					fmt.Fprintf(os.Stderr, "× %s %s без ключа\n", r.RemoteAddr, r.URL.Path)
				}
				return
			}
			fn(w, r)
		}
	}

	// enter занимает место тяжёлого запроса, когда запрос уже разобран: тело
	// читается до очереди, и медленный клиент места не держит. Ушедший клиент
	// из очереди уходит; не дождавшийся места за queueLimit получает 503
	// «занята» (решение владельца 23.10.2026: очередь — не недоступность).
	// queued — сколько ждал; оно уходит клиенту в ответе.
	slots := o.HeavySlots
	if slots <= 0 {
		slots = heavySlots
	}
	heavy := make(chan struct{}, slots)
	enter := func(w http.ResponseWriter, r *http.Request) (leave func(), queued time.Duration, ok bool) {
		select {
		case heavy <- struct{}{}:
			return func() { <-heavy }, 0, true
		default:
		}
		start := time.Now()
		var expired <-chan time.Time
		if limit := queueLimit(o.QueueWait, r); limit > 0 {
			t := time.NewTimer(limit)
			defer t.Stop()
			expired = t.C
		}
		select {
		case heavy <- struct{}{}:
			return func() { <-heavy }, time.Since(start), true
		case <-expired:
			busyError(w, slots, time.Since(start))
			return nil, 0, false
		case <-r.Context().Done():
			apiError(w, http.StatusServiceUnavailable,
				errors.New("служба занята другими запросами, а клиент перестал ждать"))
			return nil, 0, false
		}
	}

	mux.HandleFunc("/api/v1/collections", guard(func(w http.ResponseWriter, r *http.Request) {
		names, err := base.Names()
		if err != nil {
			apiError(w, http.StatusInternalServerError, err)
			return
		}
		out := make([]CollectionInfo, 0, len(names))
		for _, n := range names {
			c, err := base.Open(n)
			if err != nil {
				continue // одна испорченная коллекция не должна прятать остальные
			}
			st := c.Stats()
			out = append(out, CollectionInfo{
				Name: n, Books: st.Indexed, Chunks: st.Chunks,
				Vectors: st.Vectors, VecModel: st.VecModel, VecDim: st.VecDim,
				Generation: Generation(st),
			})
		}
		writeJSON(w, map[string]any{"collections": out, "default": def})
	}))

	mux.HandleFunc("/api/v1/search", guard(func(w http.ResponseWriter, r *http.Request) {
		var req SearchRequest
		if !readJSON(w, r, &req) {
			return
		}
		coll, name, err := pick(base, req.Collection, def)
		if err != nil {
			apiError(w, http.StatusNotFound, err)
			return
		}
		leave, queued, ok := enter(w, r)
		if !ok {
			return
		}
		defer leave()
		// Одно ядро с /search и kb_search (этап 91, R2.8).
		hits, note, err := find.Books(r.Context(), find.Deps{Coll: coll, Embedder: o.Emb, Reranker: o.Reranker,
			Translator: o.Translator},
			req.Query, req.Query, searchOpts(req, name, o))
		if err != nil {
			apiError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, SearchResponse{
			Results: hits, Note: note,
			Generation: Generation(coll.Stats()), Collection: name,
			QueuedMS: queued.Milliseconds(),
		})
	}))

	// Поиск по графу и книгам одной выдачей — команды человека /search и
	// /kb search у клиента с kb.server_url (решение владельца 23.10.2026:
	// «Команды в службу»). То же ядро find.Search и та же печать find.Render,
	// что у /search на файлах и у инструмента search службы: клиент видит ту
	// выдачу, которую увидел бы, держа библиотеку у себя.
	mux.HandleFunc("/api/v1/find", guard(func(w http.ResponseWriter, r *http.Request) {
		var req FindRequest
		if !readJSON(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Query) == "" {
			apiError(w, http.StatusBadRequest, errors.New("пустой запрос"))
			return
		}
		coll, name, err := pick(base, req.Collection, def)
		if err != nil {
			apiError(w, http.StatusNotFound, err)
			return
		}
		leave, queued, ok := enter(w, r)
		if !ok {
			return
		}
		defer leave()
		deps := find.Deps{Coll: coll, Source: coll, Embedder: o.Emb, Translator: o.Translator}
		opts := find.Opts{
			Mode: "serve", Collection: name, TableBoost: o.TableBoost,
			TopK:       clampPositive(req.TopK, maxTopK),
			MaxPerBook: clampPositive(req.MaxPerBook, maxTopK),
			MinCosine:  req.MinCosine, SemanticWeight: req.SemanticWeight,
			Entities:  clampPositive(req.Entities, maxTopK),
			Neighbors: clampPositive(req.Neighbors, maxTopK),
			Semantic:  true, QueryTimeout: kb.DefaultQueryTimeout,
			Full: req.Full, RerankOpts: o.RerankOpts,
		}
		note := ""
		if req.Rerank {
			// Молчать о несработавшем ключе нельзя: человек сравнит несравнимое.
			if o.Reranker != nil {
				deps.Reranker, opts.Rerank = o.Reranker, true
			} else {
				note = "переранжирование у службы не настроено — искал одной ступенью"
			}
		}
		if req.Graph && o.Graph != nil {
			// Графа нет — не ошибка: выдача из книг и строка об этом, как у /search.
			if g, rank, release, gerr := o.Graph.Graph(name); gerr == nil {
				defer release()
				deps.Graph, opts.Rank = g, rank
			}
		}
		res, err := find.Search(r.Context(), deps, req.Query, opts)
		if err != nil {
			apiError(w, http.StatusInternalServerError, err)
			return
		}
		text := find.Render(res, req.Full, coll)
		// Сырая выдача книг (Hits) клиенту не нужна: выдержки, сигналы
		// и напечатанный текст уже есть, а куски целиком утяжелили бы ответ вдвое.
		res.Hits = nil
		writeJSON(w, FindResponse{
			Result: res, Text: text, Note: note,
			Generation: Generation(coll.Stats()), Collection: name,
			QueuedMS: queued.Milliseconds(),
		})
	}))

	mux.HandleFunc("/api/v1/around", guard(func(w http.ResponseWriter, r *http.Request) {
		var req AroundRequest
		if !readJSON(w, r, &req) {
			return
		}
		coll, name, err := pick(base, req.Collection, def)
		if err != nil {
			apiError(w, http.StatusNotFound, err)
			return
		}
		parts, err := coll.Around(req.ID, req.Around)
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, SearchResponse{
			Results: parts, Generation: Generation(coll.Stats()), Collection: name,
		})
	}))

	// ── Граф понятий ──────────────────────────────────────────────────────
	//
	// Два входа вместо пяти. Инструменты графа (`graph_search`, `graph_entity`,
	// `graph_path`, `graph_overview`, `graph_topic`) отдают модели **готовый
	// текст**, и различаются только именем и доводами — значит одного входа
	// с именем инструмента хватает на все пять. Второй вход отдаёт разбираемый
	// результат: он нужен подмешиванию карты понятий к вопросу, где текст
	// собирается на стороне клиента.

	mux.HandleFunc("/api/v1/graph/tool", guard(func(w http.ResponseWriter, r *http.Request) {
		var req GraphToolRequest
		if !readJSON(w, r, &req) {
			return
		}
		if o.Graph == nil {
			apiError(w, http.StatusNotImplemented, errNoGraph)
			return
		}
		leave, queued, ok := enter(w, r)
		if !ok {
			return
		}
		defer leave()
		text, err := o.Graph.Tool(r.Context(), req.Collection, req.Name, req.Args)
		if errors.Is(err, ErrToolNotServed) {
			// Отдельный код, а не общий 400: «так нельзя никогда» и «довод
			// не разобрался» — разные ответы, и клиент не должен повторять
			// запрос, который служба не исполнит ни при каких доводах.
			apiError(w, http.StatusForbidden, err)
			return
		}
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, GraphToolResponse{Text: text, QueuedMS: queued.Milliseconds()})
	}))

	mux.HandleFunc("/api/v1/graph/search", guard(func(w http.ResponseWriter, r *http.Request) {
		var req GraphSearchRequest
		if !readJSON(w, r, &req) {
			return
		}
		if o.Graph == nil {
			apiError(w, http.StatusNotImplemented, errNoGraph)
			return
		}
		leave, queued, ok := enter(w, r)
		if !ok {
			return
		}
		defer leave()
		// Размер карты выбирает клиент — за контекст платит он, — но в пределах
		// службы, как top_k у поиска: ноль и меньше — «как настроено здесь».
		req.Entities = clampPositive(req.Entities, maxTopK)
		req.Neighbors = clampPositive(req.Neighbors, maxTopK)
		res, err := o.Graph.Search(r.Context(), req)
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		res.QueuedMS = queued.Milliseconds()
		writeJSON(w, res)
	}))

	mux.HandleFunc("/api/v1/books", guard(func(w http.ResponseWriter, r *http.Request) {
		coll, name, err := pick(base, r.URL.Query().Get("collection"), def)
		if err != nil {
			apiError(w, http.StatusNotFound, err)
			return
		}
		st := coll.Stats()
		writeJSON(w, map[string]any{
			"collection": name, "books": coll.Books(), "generation": Generation(st),
		})
	}))
}

// pick выбирает коллекцию: названную, иначе умолчание службы, иначе
// единственную. Отказ объясняет, что именно делать.
func pick(base *kb.Base, want, def string) (*kb.Collection, string, error) {
	name := strings.TrimSpace(want)
	if name == "" {
		name = def
	}
	if name == "" {
		names, err := base.Names()
		if err != nil {
			return nil, "", err
		}
		switch len(names) {
		case 0:
			return nil, "", fmt.Errorf("на сервере нет ни одной коллекции")
		case 1:
			name = names[0]
		default:
			return nil, "", fmt.Errorf("коллекция не названа; доступны: %s",
				strings.Join(names, ", "))
		}
	}
	c, err := base.Open(name)
	if err != nil {
		return nil, "", fmt.Errorf("коллекция %s: %w", name, err)
	}
	return c, name, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// apiError отвечает разбираемой ошибкой.
//
// Текстом, а не голым кодом: клиент показывает эту строку человеку, и «404»
// без объяснения заставляет лезть в журнал сервера.
func apiError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "нужен POST", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		apiError(w, http.StatusBadRequest, fmt.Errorf("запрос не разобрался: %w", err))
		return false
	}
	return true
}

// Auth сверяет ключ доступа.
//
// Сравнение постоянного времени: обычное сравнение строк выходит из цикла
// на первом несовпавшем знаке, и по времени ответа ключ подбирается знак
// за знаком.
//
// Пустой ключ означает «проверки нет». Это сознательное умолчание: на своей
// машине проверка ни от чего не защищает, а требовать ключ там, где он не
// нужен, значит приучать его отключать.
func Auth(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	if got == "" {
		got = strings.TrimSpace(r.Header.Get("X-Ollmcp-Token"))
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// Token читает ключ доступа службы из окружения.
//
// Только из окружения: флаг виден в `ps` любому пользователю машины, а файл
// настроек копируют и пересылают. Тем же соображением продиктовано хранение
// токена Confluence.
func Token() string { return strings.TrimSpace(os.Getenv("OLLMCP_TOKEN")) }

// GraphToolRequest — выполнить именованный инструмент графа.
type GraphToolRequest struct {
	Collection string         `json:"collection"`
	Name       string         `json:"name"`
	Args       map[string]any `json:"args,omitempty"`
}

// GraphToolResponse — тот же текст, что увидела бы модель локально.
type GraphToolResponse struct {
	Text     string `json:"text"`
	QueuedMS int64  `json:"queued_ms,omitempty"` // ожидание в очереди, мс
}

// GraphSearchRequest — разбираемый поиск по графу.
//
// Entities и Neighbors — размер карты, который выбрал клиент (его
// mix.entities и mix.neighbors); ноль — по настройкам службы. Клиент прежней
// сборки полей не шлёт и получает прежнюю карту.
type GraphSearchRequest struct {
	Collection string `json:"collection"`
	Query      string `json:"query"`
	Entities   int    `json:"entities,omitempty"`
	Neighbors  int    `json:"neighbors,omitempty"`
}

// GraphSearchResponse — карта понятий по вопросу и цепочка между двумя его
// понятиями (find.Chain по графу службы, этап 121, А6). Клиент прежней сборки
// поле chain не читает — ответ совместим.
//
// RelationYears — год самой свежей книги, подтвердившей связь, по связям
// Result.Relations в том же порядке; 0 — года нет. Прямой режим берёт его
// у своей коллекции (ChunkByRef), а у клиента службы кусков нет: без поля
// карта через службу теряла «свежайшее N г.» (аудит 23.10.2026, находка 11).
// Клиент прежней сборки поле не читает, служба прежней сборки его не шлёт —
// у связей тогда нет года, остальное то же.
type GraphSearchResponse struct {
	Result        graph.SearchResult `json:"result"`
	Chain         []graph.PathStep   `json:"chain,omitempty"`
	RelationYears []int              `json:"relation_years,omitempty"`
	QueuedMS      int64              `json:"queued_ms,omitempty"` // ожидание в очереди, мс
}

// ErrToolNotServed — служба этот инструмент не исполняет и исполнять не будет.
//
// Вход `/api/v1/graph/tool` принимает только инструменты графа: всё прочее,
// от `bash` до `write_file`, означало бы чужие руки на машине службы.
// GraphServer возвращает эту ошибку (обёрнутой), и вход отвечает 403.
var ErrToolNotServed = errors.New("служба не исполняет этот инструмент")

// errNoGraph — отказ, который объясняет себя.
//
// Служба может быть поднята без графа: он строится отдельно и часами.
// Молчаливый пустой ответ выглядел бы как «в книгах об этом не пишут».
var errNoGraph = fmt.Errorf("на этой службе граф понятий не собран или не подключён; " +
	"поиск по книгам при этом работает")
