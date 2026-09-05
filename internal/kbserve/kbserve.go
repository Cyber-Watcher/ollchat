package kbserve

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"hash/fnv"
	"net/http"
	"os"
	"strings"

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

	// Graph — как отвечать на вопросы к графу. Пусто — граф не раздаётся,
	// и клиент получит внятный отказ вместо молчания.
	Graph GraphServer

	Verbose bool
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
	// ссылки на куски. Нужен подмешиванию карты понятий к вопросу.
	Search(ctx context.Context, collection, query string) (any, error)
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
		// Одно ядро с /search и kb_search (этап 91, R2.8).
		fo := find.Opts{
			Mode: "serve", Collection: name, TableBoost: o.TableBoost,
			TopK: req.TopK, MaxPerBook: req.MaxPerBook, MinCosine: req.MinCosine,
			SemanticWeight: req.SemanticWeight, Docs: req.Books, Exact: req.Exact,
			Semantic: true, QueryTimeout: kb.DefaultQueryTimeout,
			Rerank: true, RerankOpts: o.RerankOpts,
		}
		hits, note, err := find.Books(r.Context(), find.Deps{Coll: coll, Embedder: o.Emb, Reranker: o.Reranker},
			req.Query, req.Query, fo)
		if err != nil {
			apiError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, SearchResponse{
			Results: hits, Note: note,
			Generation: Generation(coll.Stats()), Collection: name,
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
		text, err := o.Graph.Tool(r.Context(), req.Collection, req.Name, req.Args)
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, GraphToolResponse{Text: text})
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
		res, err := o.Graph.Search(r.Context(), req.Collection, req.Query)
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"result": res})
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
	Text string `json:"text"`
}

// GraphSearchRequest — разбираемый поиск по графу.
type GraphSearchRequest struct {
	Collection string `json:"collection"`
	Query      string `json:"query"`
}

// errNoGraph — отказ, который объясняет себя.
//
// Служба может быть поднята без графа: он строится отдельно и часами.
// Молчаливый пустой ответ выглядел бы как «в книгах об этом не пишут».
var errNoGraph = fmt.Errorf("на этой службе граф понятий не собран или не подключён; " +
	"поиск по книгам при этом работает")
