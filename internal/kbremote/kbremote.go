package kbremote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Общая библиотека организации: те же книги, но по сети.
//
// **Зачем.** Для одного человека коллекция — каталог файлов рядом с ним, и это
// лучший из возможных вариантов: ни служб, ни портов, ни бэкапов. В организации
// так не выходит: индекс собирают часами на видеокарте, и делать это на каждом
// рабочем месте — расточительство, а держать библиотеку в общей папке нельзя,
// потому что поиск читает индекс целиком в память.
//
// Поэтому: администратор собирает знание один раз и поднимает `ollmcp --http`,
// сотрудники читают готовое. Пишет один, читают все — намеренно: индексация,
// счёт векторов и сборка графа остаются местными операциями над файлами,
// и удалённый клиент их не то что не должен, а физически не может выполнить.
//
// **Здесь только клиент.** Драйвер удовлетворяет `kb.Source`, и всё, что зовёт
// поиск по книгам — инструменты модели, подмешивание к вопросу, команда
// `/kb search`, — не замечает разницы. Ни одного условия «если сеть» в тех
// местах не появилось.
//
// **Граф тоже приходит по сети.** Инструменты графа выполняются на службе
// и отдают клиенту готовый текст — тот самый, что увидела бы модель локально;
// подмешиванию карты понятий отдаётся разбираемый результат. Пересылать сам
// граф не нужно и незачем: он живёт там, где собран.
//
// **Вектор вопроса считает сервер.** У клиента нет ни адреса Ollama, ни имени
// модели эмбеддера — и рассогласоваться моделям негде. Это же означает, что
// эмбеддер, переданный в `SearchWith`, здесь не используется; подпись оставлена
// общей ради интерфейса.

// Client — соединение с общей библиотекой.
type Client struct {
	base    string
	token   string
	http    *http.Client
	timeout time.Duration
}

// Opts — как соединяться.
type Opts struct {
	// URL службы, например http://kb.corp.local:8377. Пусто — драйвер не нужен.
	URL string

	// Token — ключ доступа. По политике проекта в файл настроек не пишется:
	// берётся из файла, переменной окружения или команды — так же, как токен
	// Confluence.
	Token string

	// Timeout — предел ожидания ответа. 0 — тридцать секунд: поиск по чужой
	// библиотеке идёт дольше местного, но не минутами.
	Timeout time.Duration
}

// New заводит клиента. Пустой адрес — не ошибка, а «работаем с файлами».
//
// **Ключа в адресе нет** (решение владельца 23.10.2026: «ключ в адресе
// не поддерживаем»). Учётные данные (`user:pass@`) и строка запроса (`?key=…`)
// в kb.server_url — отказ: адрес службы входит в текст ошибок, а их видят
// модель, лента, журнал шагов и JSON `--ask` (аудит 23.10.2026, находка 8).
// Ключ доступа — Opts.Token (kb.server_token_file). Отказ адреса с ключом
// не повторяет: печатается адрес без него.
func New(o Opts) (*Client, error) {
	addr := strings.TrimSpace(o.URL)
	if addr == "" {
		return nil, nil
	}
	if err := CheckURL(addr); err != nil {
		return nil, err
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		base:    strings.TrimRight(addr, "/"),
		token:   strings.TrimSpace(o.Token),
		http:    &http.Client{Timeout: timeout},
		timeout: timeout,
	}, nil
}

// CheckURL проверяет kb.server_url так же, как New: адрес вида
// http://машина:порт[/путь] без учётных данных, строки запроса и якоря. Для
// тех, кто строит из этого адреса свои (сервер kb хаба MCP: `<адрес>/mcp`) и
// должен отказать тем же словами, а не дописать путь к строке запроса.
// Пустой адрес — не ошибка. Отказ ключа не повторяет.
func CheckURL(raw string) error {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return nil
	}
	u, err := url.Parse(addr)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("kb.server_url = %q: нужен адрес вида http://машина:порт", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		clean := *u
		clean.User, clean.RawQuery, clean.ForceQuery, clean.Fragment, clean.RawFragment = nil, "", false, "", ""
		return fmt.Errorf("kb.server_url = %s…: учётные данные (user:pass@), строка запроса (?…) "+
			"и якорь (#…) в адресе службы не поддерживаются — ключ доступа задаётся kb.server_token_file",
			clean.String())
	}
	return nil
}

// Names — какие коллекции есть на сервере.
func (c *Client) Names() ([]string, error) {
	list, _, err := c.collections(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list))
	for _, ci := range list {
		out = append(out, ci.Name)
	}
	return out, nil
}

// Source открывает коллекцию на чтение.
//
// Состав коллекции забирается сразу: `Stats` и `Books` в интерфейсе объявлены
// без ошибки и без контекста, а сходить по сети из них было бы нельзя.
// Заодно это проверка связи — «сервер недоступен» лучше узнать при открытии,
// чем на первом вопросе человека.
func (c *Client) Source(name string) (kb.Source, error) {
	list, def, err := c.collections(context.Background())
	if err != nil {
		return nil, err
	}
	want := strings.TrimSpace(name)
	if want == "" {
		want = def
	}
	if want == "" && len(list) == 1 {
		want = list[0].Name
	}
	for _, ci := range list {
		if ci.Name == want {
			s := &source{cl: c, name: ci.Name}
			if err := s.refresh(context.Background(), ci); err != nil {
				return nil, err
			}
			return s, nil
		}
	}
	names := make([]string, 0, len(list))
	for _, ci := range list {
		names = append(names, ci.Name)
	}
	return nil, fmt.Errorf("на сервере %s нет коллекции %q; доступны: %s",
		c.base, name, strings.Join(names, ", "))
}

// source — одна коллекция общей библиотеки.
type source struct {
	cl   *Client
	name string

	mu    sync.RWMutex
	stats kb.Stats
	books []kb.BookRec
	gen   string
	note  string
	// queued — сколько вызовы этого источника ждали места в очереди службы.
	// Источник заводится на одно действие (kb.Fallback), так что это время
	// одного подмеса или одного вызова инструмента.
	queued time.Duration
}

func (s *source) Name() string { return s.name }

// Queued — сколько вызовы источника ждали очереди службы (kb.Queued).
func (s *source) Queued() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queued
}

// waited прибавляет ожидание в очереди из ответа службы.
func (s *source) waited(ms int64) {
	if ms <= 0 {
		return
	}
	s.mu.Lock()
	s.queued += time.Duration(ms) * time.Millisecond
	s.mu.Unlock()
}

func (s *source) Stats() kb.Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats
}

func (s *source) Books() []kb.BookRec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]kb.BookRec(nil), s.books...)
}

func (s *source) SearchNote() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.note
}

// SearchWith ищет по общей библиотеке.
//
// Эмбеддер не используется: вектор вопроса считает сервер, у которого он
// настроен. Довод оставлен ради общей подписи интерфейса.
func (s *source) SearchWith(ctx context.Context, query string, opt kb.SearchOpts, _ kb.Embedder) ([]kb.Result, error) {
	req := searchRequest{
		Collection: s.name, Query: query,
		TopK: opt.TopK, MaxPerBook: opt.MaxPerDoc,
		MinCosine: opt.MinCosine, SemanticWeight: opt.SemanticWeight,
		Books: opt.Docs, Exact: opt.Exact,
	}
	var resp searchResponse
	if err := s.cl.post(ctx, "/api/v1/search", req, &resp); err != nil {
		return nil, err
	}
	s.waited(resp.QueuedMS)
	s.afterCall(ctx, resp.Generation, resp.Note)
	return resp.Results, nil
}

// Around отдаёт кусок целиком с соседями.
func (s *source) Around(id string, around int) ([]kb.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cl.timeout)
	defer cancel()
	var resp searchResponse
	err := s.cl.post(ctx, "/api/v1/around",
		aroundRequest{Collection: s.name, ID: id, Around: around}, &resp)
	if err != nil {
		return nil, err
	}
	s.waited(resp.QueuedMS)
	s.afterCall(ctx, resp.Generation, "")
	return resp.Results, nil
}

// afterCall обновляет состав, если сервер сообщил о другом поколении индекса.
//
// **Зачем поколение.** Локально свежесть видна по файлам; по сети — нет.
// Администратор долил книги, а клиент продолжал бы показывать вчерашний состав
// и старое число книг в отчётах. Ровно эта беда уже случалась с долгоживущей
// службой (`internal/kb/reopen.go`), и повторять её незачем.
//
// Состав перечитывается **после** ответа и не мешает выдаче: сама выдача
// уже пришла из свежего индекса, устареть могли только цифры рядом с ней.
func (s *source) afterCall(ctx context.Context, gen, note string) {
	s.mu.Lock()
	s.note = note
	same := gen == "" || gen == s.gen
	s.mu.Unlock()
	if same {
		return
	}
	list, _, err := s.cl.collections(ctx)
	if err != nil {
		return // не беда: цифры отстанут до следующего запроса
	}
	for _, ci := range list {
		if ci.Name == s.name {
			_ = s.refresh(ctx, ci) // не вышло — повторим на следующем ответе
			return
		}
	}
}

// refresh читает реестр книг и только после него принимает новое поколение:
// поколение, состав и книги меняются вместе.
//
// Раньше поколение ставилось до загрузки книг. Сбой загрузки оставлял старый
// реестр, а следующий ответ с тем же поколением считался уже учтённым —
// и книги не перечитывались до следующей доливки на сервере.
func (s *source) refresh(ctx context.Context, ci collectionInfo) error {
	var resp struct {
		Books []kb.BookRec `json:"books"`
	}
	if err := s.cl.get(ctx, "/api/v1/books?collection="+url.QueryEscape(s.name), &resp); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen = ci.Generation
	s.stats = kb.Stats{
		Books: ci.Books, Indexed: ci.Books, Chunks: ci.Chunks,
		Vectors: ci.Vectors, VecModel: ci.VecModel, VecDim: ci.VecDim,
	}
	s.books = resp.Books
	return nil
}

// collections спрашивает состав библиотеки.
func (c *Client) collections(ctx context.Context) ([]collectionInfo, string, error) {
	var resp struct {
		Collections []collectionInfo `json:"collections"`
		Default     string           `json:"default"`
	}
	if err := c.get(ctx, "/api/v1/collections", &resp); err != nil {
		return nil, "", err
	}
	return resp.Collections, resp.Default, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

// do выполняет запрос и разбирает ответ.
//
// Ошибки называют адрес службы: человеку, у которого «поиск перестал работать»,
// надо сразу видеть, что дело в чужой машине, а не в его книгах.
func (c *Client) do(req *http.Request, out any) error {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set(queueWaitHeader, strconv.FormatInt(c.queueWait(req.Context()).Milliseconds(), 10))
	resp, err := c.http.Do(req)
	if err != nil {
		return transport(req, fmt.Errorf("библиотека %s недоступна: %w", c.base, err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return transport(req, fmt.Errorf("библиотека %s: ответ не дочитался: %w", c.base, err))
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error    string `json:"error"`
			Busy     bool   `json:"busy"`
			QueuedMS int64  `json:"queued_ms"`
		}
		parsed := json.Unmarshal(body, &e) == nil
		if resp.StatusCode == http.StatusServiceUnavailable && parsed && e.Busy {
			// Служба жива, но занята: не недоступность (решение владельца
			// 23.10.2026) — на свои файлы клиент не переходит, а говорит об очереди.
			return busy{fmt.Errorf("библиотека %s: %w — очередь службы полна, ждал в очереди %d мс; повторите позже",
				c.base, kb.ErrBusy, e.QueuedMS)}
		}
		if parsed && e.Error != "" {
			return fmt.Errorf("библиотека %s: %s", c.base, e.Error)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("библиотека %s: нужен ключ доступа (kb.server_token_file)", c.base)
		}
		if resp.StatusCode == http.StatusNotFound {
			// Вход без разбираемой ошибки — его нет у службы прежней сборки.
			return fmt.Errorf("библиотека %s: %w (%s)", c.base, ErrNotServed, req.URL.Path)
		}
		return fmt.Errorf("библиотека %s ответила %s", c.base, resp.Status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("библиотека %s: ответ не разобрался: %w", c.base, err)
	}
	return nil
}

// transport помечает ошибку, при которой служба не дала ответа (отказ
// в соединении, срок, обрыв), как kb.ErrUnreachable: по этой метке клиент
// переходит на свои файлы (этап 121, А4). Ответ со статусом — 401, 404,
// «нет такой коллекции», 501 «граф не подключён» — сюда не попадает: это
// отказ по смыслу, и переход его спрятал бы.
//
// Отмена вызывающим — человек нажал Esc — не недоступность службы: искать
// у себя то, что человек сам бросил, незачем.
func transport(req *http.Request, err error) error {
	if errors.Is(req.Context().Err(), context.Canceled) {
		return err
	}
	return unreachable{err}
}

// unreachable — ошибка службы, не давшей ответа. Текст прежний (адрес
// и причина), метка — kb.ErrUnreachable.
type unreachable struct{ err error }

func (e unreachable) Error() string   { return e.err.Error() }
func (e unreachable) Unwrap() []error { return []error{kb.ErrUnreachable, e.err} }

// busy — служба ответила 503 с признаком «занята»: метка kb.ErrBusy внутри
// текста, kb.ErrUnreachable нет — переход на свои файлы по ней не идёт.
type busy struct{ err error }

func (e busy) Error() string { return e.err.Error() }
func (e busy) Unwrap() error { return e.err }

// ErrNotServed — вход службе неизвестен (404 без разбираемой ошибки): служба
// прежней сборки. Клиент обходится тем, что у неё есть.
var ErrNotServed = errors.New("у службы нет такого входа — служба прежней сборки")

// queueWaitHeader — kbserve.QueueWaitHeader: сколько клиент готов ждать места
// в очереди службы, мс. Повторён здесь по той же причине, что формы ниже.
const queueWaitHeader = "X-Ollchat-Queue-Wait-Ms"

// queueWait — сколько просить службу ждать места в очереди: половину того,
// что осталось до своего срока (kb.server_timeout или срок вызывающего).
// Вторая половина — на сам поиск: служба отвечает «занята» раньше, чем клиент
// сочтёт её недоступной, и очередь не превращается в переход на свои файлы.
func (c *Client) queueWait(ctx context.Context) time.Duration {
	left := c.timeout
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d < left {
			left = d
		}
	}
	if wait := left / 2; wait >= time.Millisecond {
		return wait
	}
	return time.Millisecond
}

// Формы запросов и ответов повторяют серверные (`internal/kbserve/kbserve.go`).
// Дублирование намеренное: связать их общим пакетом значило бы, что клиент
// и служба обязаны обновляться вместе, а они живут на разных машинах и
// обновляются порознь.

type searchRequest struct {
	Collection     string   `json:"collection"`
	Query          string   `json:"query"`
	TopK           int      `json:"top_k,omitempty"`
	MaxPerBook     int      `json:"max_per_book,omitempty"`
	MinCosine      float64  `json:"min_cosine,omitempty"`
	SemanticWeight float64  `json:"semantic_weight,omitempty"`
	Books          []uint32 `json:"books,omitempty"`
	Exact          string   `json:"exact,omitempty"`
}

type aroundRequest struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Around     int    `json:"around,omitempty"`
}

type searchResponse struct {
	Results    []kb.Result `json:"results"`
	Note       string      `json:"note,omitempty"`
	Generation string      `json:"generation"`
	Collection string      `json:"collection"`
	QueuedMS   int64       `json:"queued_ms,omitempty"`
}

type collectionInfo struct {
	Name       string `json:"name"`
	Books      int    `json:"books"`
	Chunks     int    `json:"chunks"`
	Vectors    int    `json:"vectors"`
	VecModel   string `json:"vec_model,omitempty"`
	VecDim     int    `json:"vec_dim,omitempty"`
	Generation string `json:"generation"`
}

// Проверка во время сборки: драйвер обязан оставаться взаимозаменяемым
// с локальной коллекцией.
var (
	_ kb.Source  = (*source)(nil)
	_ kb.Library = (*Client)(nil)
)

// GraphTool выполняет инструмент графа на стороне службы.
//
// Возвращается **тот же текст**, который модель увидела бы, работая с файлами:
// службу и клиента обслуживает одна реализация инструментов, второй нет.
//
// Ждал запрос места в очереди службы — первой строкой текста «(служба
// занята: ждал в очереди N мс)»: модель и человек видят, почему ответ шёл
// долго (решение владельца 23.10.2026). Занята настолько, что места не
// нашлось, — ошибка с меткой kb.ErrBusy, на свои файлы по ней не переходят.
func (c *Client) GraphTool(ctx context.Context, collection, name string, args map[string]any) (string, error) {
	var resp struct {
		Text     string `json:"text"`
		QueuedMS int64  `json:"queued_ms,omitempty"`
	}
	err := c.post(ctx, "/api/v1/graph/tool", map[string]any{
		"collection": collection, "name": name, "args": args,
	}, &resp)
	if err != nil {
		return "", err
	}
	if note := kb.QueueNote(time.Duration(resp.QueuedMS) * time.Millisecond); note != "" {
		return "(" + note + ")\n" + resp.Text, nil
	}
	return resp.Text, nil
}

// GraphSearch отдаёт разбираемый результат поиска по графу и цепочку между
// двумя понятиями вопроса — то же, что GraphMap, без годов связей и времени
// в очереди (ключ --kb-remote-check).
func (c *Client) GraphSearch(ctx context.Context, collection, query string, entities, neighbors int) (graph.SearchResult, []graph.PathStep, error) {
	m, err := c.GraphMap(ctx, collection, query, entities, neighbors)
	return m.Result, m.Chain, err
}

// GraphMap — карта понятий от службы со всем, что к ней прилагается.
type GraphMap struct {
	Result graph.SearchResult
	Chain  []graph.PathStep

	// Years — год самой свежей книги у каждой связи Result.Relations, в том же
	// порядке; 0 — года нет. nil — служба прежней сборки или годов нет вовсе:
	// у связей тогда нет «свежайшее N г.», остальное то же.
	Years []int

	// Queued — сколько запрос ждал места в очереди службы.
	Queued time.Duration
}

// GraphMap отдаёт разбираемый результат поиска по графу, цепочку между двумя
// понятиями вопроса и годы связей.
//
// Нужен подмешиванию карты понятий к вопросу: размер карты решает, сколько
// контекста уйдёт на каждый вопрос, и решать это должен тот, кто за контекст
// платит, — клиент. Поэтому entities и neighbors (mix.entities, mix.neighbors
// клиента) уходят службе; ноль — «как настроено у службы», и тогда поля
// в запросе нет вовсе: служба прежней сборки получает тот же запрос, что раньше.
// Ранжирование связей (/graph tune, graph.neighbor_*) и смысловой вход
// (mix.sense_entry) — по настройкам службы: в запросе их нет.
//
// Цепочку служба считает тем же find.Chain по своему графу (этап 121, А6).
// Служба прежней сборки поля chain не отдаёт — цепочка пустая, карта та же.
func (c *Client) GraphMap(ctx context.Context, collection, query string, entities, neighbors int) (GraphMap, error) {
	var resp graphSearchResponse
	err := c.post(ctx, "/api/v1/graph/search", graphSearchRequest{
		Collection: collection, Query: query, Entities: entities, Neighbors: neighbors,
	}, &resp)
	if err != nil {
		return GraphMap{}, err
	}
	years := resp.RelationYears
	if len(years) != len(resp.Result.Relations) {
		years = nil // не по связям — года не к чему приложить
	}
	return GraphMap{Result: resp.Result, Chain: resp.Chain, Years: years,
		Queued: time.Duration(resp.QueuedMS) * time.Millisecond}, nil
}

// FindRequest — поиск по графу и книгам одной выдачей у службы: команды
// человека /search (Graph) и /kb search (без графа). Числа — живые числа
// клиента (/kb tune); ноль — как настроено у службы.
type FindRequest struct {
	Collection     string  `json:"collection"`
	Query          string  `json:"query"`
	TopK           int     `json:"top_k,omitempty"`
	MaxPerBook     int     `json:"max_per_book,omitempty"`
	MinCosine      float64 `json:"min_cosine,omitempty"`
	SemanticWeight float64 `json:"semantic_weight,omitempty"`
	Entities       int     `json:"entities,omitempty"`
	Neighbors      int     `json:"neighbors,omitempty"`
	Graph          bool    `json:"graph,omitempty"`
	Rerank         bool    `json:"rerank,omitempty"`
	Full           bool    `json:"full,omitempty"`
}

// FindReply — выдача службы: разобранная (для /read N и Ctrl+F) и
// напечатанная её файлами (Text — выдержки под связями и годы связей).
type FindReply struct {
	Result     find.Result
	Text       string
	Note       string // оговорка службы: переранжирование не настроено и подобное
	Collection string
	Queued     time.Duration
}

// Find — поиск /search у службы (вход /api/v1/find, решение владельца
// 23.10.2026: «Команды в службу»). Служба прежней сборки входа не знает —
// ошибка с меткой ErrNotServed.
func (c *Client) Find(ctx context.Context, req FindRequest) (FindReply, error) {
	var resp struct {
		Result     find.Result `json:"result"`
		Text       string      `json:"text"`
		Note       string      `json:"note,omitempty"`
		Collection string      `json:"collection"`
		QueuedMS   int64       `json:"queued_ms,omitempty"`
	}
	if err := c.post(ctx, "/api/v1/find", req, &resp); err != nil {
		return FindReply{}, err
	}
	return FindReply{Result: resp.Result, Text: resp.Text, Note: resp.Note, Collection: resp.Collection,
		Queued: time.Duration(resp.QueuedMS) * time.Millisecond}, nil
}

// graphSearchRequest и graphSearchResponse повторяют серверные
// (`kbserve.GraphSearchRequest`, `kbserve.GraphSearchResponse`) — по той же
// причине, что формы поиска выше.
type graphSearchRequest struct {
	Collection string `json:"collection"`
	Query      string `json:"query"`
	Entities   int    `json:"entities,omitempty"`
	Neighbors  int    `json:"neighbors,omitempty"`
}

type graphSearchResponse struct {
	Result        graph.SearchResult `json:"result"`
	Chain         []graph.PathStep   `json:"chain"`
	RelationYears []int              `json:"relation_years,omitempty"`
	QueuedMS      int64              `json:"queued_ms,omitempty"`
}
