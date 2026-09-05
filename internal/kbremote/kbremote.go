package kbremote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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
func New(o Opts) (*Client, error) {
	addr := strings.TrimSpace(o.URL)
	if addr == "" {
		return nil, nil
	}
	u, err := url.Parse(addr)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("kb.server_url = %q: нужен адрес вида http://машина:порт", o.URL)
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
			s.apply(ci)
			if err := s.loadBooks(context.Background()); err != nil {
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
}

func (s *source) Name() string { return s.name }

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
			s.apply(ci)
			_ = s.loadBooks(ctx)
			return
		}
	}
}

func (s *source) apply(ci collectionInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen = ci.Generation
	s.stats = kb.Stats{
		Books: ci.Books, Indexed: ci.Books, Chunks: ci.Chunks,
		Vectors: ci.Vectors, VecModel: ci.VecModel, VecDim: ci.VecDim,
	}
}

func (s *source) loadBooks(ctx context.Context) error {
	var resp struct {
		Books []kb.BookRec `json:"books"`
	}
	if err := s.cl.get(ctx, "/api/v1/books?collection="+url.QueryEscape(s.name), &resp); err != nil {
		return err
	}
	s.mu.Lock()
	s.books = resp.Books
	s.mu.Unlock()
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
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("библиотека %s недоступна: %w", c.base, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("библиотека %s: ответ не дочитался: %w", c.base, err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return fmt.Errorf("библиотека %s: %s", c.base, e.Error)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("библиотека %s: нужен ключ доступа (kb.server_token_file)", c.base)
		}
		return fmt.Errorf("библиотека %s ответила %s", c.base, resp.Status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("библиотека %s: ответ не разобрался: %w", c.base, err)
	}
	return nil
}

// Формы запросов и ответов повторяют серверные (`ollmcp/dataapi.go`).
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
func (c *Client) GraphTool(ctx context.Context, collection, name string, args map[string]any) (string, error) {
	var resp struct {
		Text string `json:"text"`
	}
	err := c.post(ctx, "/api/v1/graph/tool", map[string]any{
		"collection": collection, "name": name, "args": args,
	}, &resp)
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// GraphSearch отдаёт разбираемый результат поиска по графу.
//
// Нужен подмешиванию карты понятий к вопросу: размер карты решает, сколько
// контекста уйдёт на каждый вопрос, и решать это должен тот, кто за контекст
// платит, — клиент.
func (c *Client) GraphSearch(ctx context.Context, collection, query string) (graph.SearchResult, error) {
	var resp struct {
		Result graph.SearchResult `json:"result"`
	}
	err := c.post(ctx, "/api/v1/graph/search", map[string]any{
		"collection": collection, "query": query,
	}, &resp)
	return resp.Result, err
}
