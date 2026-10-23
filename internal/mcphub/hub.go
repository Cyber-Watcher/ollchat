// Package mcphub — сторонние серверы MCP из конфига ([mcp.servers.<имя>]):
// подключение, их инструменты в реестре модели, состояние для команды /mcp
// и ключей --mcp-* (этап 121, Б3–Б6, В1–В5, В7).
//
// **Зачем отдельный пакет.** Клиент протокола (`internal/mcpclient`) не знает
// ни реестра, ни прав, ни конфига; реестр (`internal/tools`) не может звать
// клиента напрямую — `internal/mcp` импортирует `tools`, тесты клиента
// поднимают `internal/mcp`, и прямой импорт замкнул бы круг. Здесь они
// встречаются: один экземпляр Hub на процесс, его читают и TUI, и ключи
// командной строки — логика общая, а не две копии.
//
// **Решения владельца 22.10.2026.** Транспорт только HTTP; имена
// mcp__<сервер>__<инструмент>, встроенные не заменяются; описания и вывод —
// недоверенный ввод; любой вызов — через подтверждение (вид Fetch, цель
// mcp://<сервер>/<инструмент>), без вопроса — только allow сервера и yolo,
// noask спрашивает; инструменты самой службы ollmcp (kb.server_url) модели
// вторым набором не отдаются — она получает их под встроенными именами;
// исключение — перечисленные в tools службы и без встроенного двойника (kb_status).
package mcphub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kbremote"
	"github.com/Cyber-Watcher/ollchat/internal/mcpclient"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// ConnectTimeout — общий срок подключения ко всем серверам при запуске.
// Недоступный сервер задерживает запуск не дольше него, а не на свой срок
// обмена (60 с по умолчанию): его инструментов просто нет до /mcp reconnect.
const ConnectTimeout = 3 * time.Second

// LibraryServer — имя, под которым ключам --mcp-* и /mcp видна служба общей
// библиотеки (kb.server_url), если в [mcp.servers] она не описана сама.
const LibraryServer = "kb"

// Пределы того, что сервер пишет модели о себе: описание — недоверенный
// текст и место в каждом запросе, схема — тоже.
const (
	maxDescription = 1024     // знаков описания вместе с пометкой сервера
	maxSchemaBytes = 32 << 10 // байт схемы параметров одного инструмента
	// maxServerTools и maxServerSpecs — один бюджет сервера на всё, что он
	// кладёт в каждый запрос к модели: число инструментов и байты описаний
	// вместе со схемами. Без него сервер с тысячей инструментов занял бы
	// контекст до вопроса; до 23.10.2026 предел считал одни схемы, и 3 000
	// инструментов с длинными описаниями проходили все (аудит, находка 4).
	// Числа выбраны, а не замерены: 64 инструмента и две схемы предельного
	// размера. Что не влезло — в отброшенные с причиной и подсказкой
	// перечислить нужные в tools.
	maxServerTools = 64
	maxServerSpecs = 2 * maxSchemaBytes
)

// closeTimeout — срок, за который закрывается брошенный сеанс сервера
// (DELETE): сервер мог и пропасть, ждать его срок обмена незачем.
const closeTimeout = 2 * time.Second

// Состояния сервера.
const (
	stateOff   = "выключен (enabled = false)"
	stateNever = "не подключался"
	stateUp    = "подключён"
	stateDown  = "ошибка"
)

// Hub — серверы MCP одного процесса.
type Hub struct {
	version  string
	maxOutKB int

	// pubMu — снимок инструментов сервера и его передача в реестр идут одним
	// шагом (publish); берётся раньше mu и не держится под ним.
	pubMu sync.Mutex

	mu      sync.Mutex
	names   []string // по алфавиту, как в конфиге (config.MCP.Names), служба последней
	servers map[string]*server
	reg     *tools.Registry
}

// server — один сервер и всё, что о нём известно за сеанс. Поля после mu —
// под Hub.mu.
type server struct {
	name    string
	cfg     config.MCPServer
	where   string // адрес без строки запроса и учётных данных
	allow   map[string]bool
	visible map[string]bool // nil — все
	// library — это служба общей библиотеки (kb.server_url): её инструменты
	// модель получает под встроенными именами; в реестр идут только явно
	// перечисленные в tools и без встроенного двойника (exposed).
	library bool
	// token — откуда ключ: у службы — kb.server_token_*, у прочих — свои поля.
	token func(ctx context.Context) (string, error)

	client  *mcpclient.Client
	state   string
	err     error
	info    mcpclient.Info
	hasInfo bool
	tools   []entry
	skipped []string
	listed  time.Time
	// first — отпечаток описания и схемы при первом чтении за сеанс; changed —
	// инструменты, у которых он с тех пор поменялся (Б5): до перезапуска они
	// спрашивают, даже если перечислены в allow.
	first   map[string]string
	changed map[string]bool
	// busy — сколько вызовов и перечитываний идёт каждым клиентом: прежний
	// клиент после переподключения закрывается, только когда им никто не
	// пользуется — DELETE посреди вызова заставил бы клиента поздороваться
	// заново и послать tools/call второй раз. pending — сколько перечитываний
	// списка ещё идёт: пока они не кончились, вызовы сервера спрашивают.
	busy    map[*mcpclient.Client]int
	pending int
}

// entry — инструмент сервера, как его увидит модель.
type entry struct {
	remote string // имя у сервера
	name   string // mcp__<сервер>__<имя>
	desc   string // с пометкой сервера, очищенное и урезанное
	params ollama.ToolParams
}

// New собирает серверы из конфига. Сеть не трогает: подключение — Connect,
// Reconnect или Ping.
//
// version — что ollchat называет о себе серверу (clientInfo.version); пусто —
// версия сборки.
func New(cfg *config.Config, version string) *Hub {
	h := &Hub{version: version, maxOutKB: cfg.Agent.MaxOutputKB, servers: map[string]*server{}}
	if h.maxOutKB <= 0 {
		h.maxOutKB = 64 // как у реестра (tools.NewRegistry)
	}
	libURL := ""
	if u := strings.TrimSpace(cfg.KB.ServerURL); u != "" {
		libURL = libraryURL(u)
	}
	for _, name := range cfg.MCP.Names() {
		sc := cfg.MCP.Servers[name]
		s := newServer(name, sc)
		s.library = libURL != "" && sameURL(sc.URL, libURL)
		file, env, cmd := sc.TokenFile, sc.TokenEnv, sc.TokenCmd
		s.token = func(ctx context.Context) (string, error) { return mcpclient.Token(ctx, file, env, cmd) }
		h.add(s)
	}
	// Служба библиотеки видна ключам как любой сервер (этап 121, Б), даже не
	// описанная в [mcp.servers]: ключ у неё тот же, что у kb.server_url.
	if libURL != "" && !h.hasLibrary() {
		if _, taken := h.servers[LibraryServer]; !taken {
			// Срок — тот же, что у клиента библиотеки (пусто — 30 с, как в
			// docs/configuration.md); сырую строку конфиг уже проверил, а
			// готовая длительность не даст MCPServer подставить свои 60 с.
			s := newServer(LibraryServer, config.MCPServer{URL: libURL, Timeout: cfg.KB.ServerTimeoutDuration().String()})
			s.library = true
			kbc := cfg.KB
			s.token = func(ctx context.Context) (string, error) { return kbremote.Token(ctx, kbc) }
			h.add(s)
		}
	}
	return h
}

func newServer(name string, sc config.MCPServer) *server {
	s := &server{name: name, cfg: sc, where: CleanURL(sc.URL), allow: map[string]bool{},
		state: stateNever, first: map[string]string{}, changed: map[string]bool{}, busy: map[*mcpclient.Client]int{}}
	if !sc.On() {
		s.state = stateOff
	}
	for _, a := range sc.Allow {
		s.allow[a] = true
	}
	if len(sc.Tools) > 0 {
		s.visible = map[string]bool{}
		for _, t := range sc.Tools {
			s.visible[t] = true
		}
	}
	return s
}

func (h *Hub) add(s *server) {
	h.servers[s.name] = s
	h.names = append(h.names, s.name)
}

func (h *Hub) hasLibrary() bool {
	for _, s := range h.servers {
		if s.library {
			return true
		}
	}
	return false
}

// exposed — отдаются ли инструменты сервера модели. Служба библиотеки
// (kb.server_url) — только перечисленные в её tools явно и без дублей
// встроенных (search, kb_read, graph_* модель получает под своими именами).
// Слово владельца 22.10.2026: у kb_status встроенного двойника нет, а в режиме
// библиотеки он пропадал у модели вместе с дублями.
func (s *server) exposed() bool { return !s.library || s.visible != nil }

// builtinTools — имена встроенных инструментов: у службы библиотеки они
// дубли того, что модель и так получает.
var builtinTools = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range tools.AllNames() {
		m[n] = true
	}
	return m
}()

// LibraryName — под каким именем видна служба общей библиотеки
// (kb.server_url); пусто — она не настроена.
func (h *Hub) LibraryName() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, name := range h.names {
		if h.servers[name].library {
			return name
		}
	}
	return ""
}

// Connectable — есть ли что подключать при запуске: включённые серверы,
// кроме службы библиотеки (её инструменты у модели встроенные).
func (h *Hub) Connectable() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, name := range h.names {
		if s := h.servers[name]; s.cfg.On() && s.exposed() {
			return true
		}
	}
	return false
}

// Empty — серверов нет вовсе: ни в конфиге, ни службы библиотеки.
func (h *Hub) Empty() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.names) == 0
}

// Attach подключает реестр модели: инструменты серверов, уже прочитанные
// и прочитанные потом, попадают в него (SetExternal).
func (h *Hub) Attach(reg *tools.Registry) error {
	h.mu.Lock()
	h.reg = reg
	h.mu.Unlock()
	for _, name := range h.Names() {
		if err := h.publish(name); err != nil {
			return err
		}
	}
	return nil
}

// Names — имена серверов в порядке показа.
func (h *Hub) Names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.names...)
}

// AllowRules — правила allow для набора прав (вид Fetch, цель
// mcp://<сервер>/<инструмент>) по спискам allow включённых серверов. Их
// кладут в Set при сборке охранника — до первого вызова модели.
func (h *Hub) AllowRules() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, name := range h.names {
		s := h.servers[name]
		if !s.cfg.On() {
			continue
		}
		for _, a := range s.cfg.Allow {
			out = append(out, "Fetch("+Target(name, a)+")")
		}
	}
	return out
}

// Target — цель проверки прав для инструмента tool сервера server.
func Target(server, tool string) string { return "mcp://" + server + "/" + tool }

// Connect подключается ко всем включённым серверам разом — тем, чьи
// инструменты получает модель. Ждёт не дольше срока ctx: кто не успел,
// остаётся с ошибкой, и запуск идёт дальше. Служба библиотеки здесь не
// трогается — её инструменты у модели встроенные.
func (h *Hub) Connect(ctx context.Context) {
	var wg sync.WaitGroup
	for _, name := range h.Names() {
		s := h.server(name)
		if s == nil || !s.cfg.On() || !s.exposed() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.connect(ctx, s)
		}()
	}
	wg.Wait()
}

// Reconnect подключается к серверу name заново (пусто — ко всем включённым):
// новое рукопожатие, свежий список инструментов. Ошибка — у названного
// сервера; для всех — первая из ошибок, состояние каждого видно в Report.
func (h *Hub) Reconnect(ctx context.Context, name string) error {
	if name == "" {
		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			first error
		)
		for _, n := range h.Names() {
			s := h.server(n)
			if s == nil || !s.cfg.On() || !s.exposed() {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := h.connect(ctx, s); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		return first
	}
	s, err := h.lookup(name)
	if err != nil {
		return err
	}
	return h.connect(ctx, s)
}

// Ping — рукопожатие с сервером name: сведения о нём и сколько оно шло.
// Список инструментов не читает. Ошибку читает человек (--mcp-ping,
// --kb-remote-check): если сервер перенаправляет, она называет новый адрес.
func (h *Hub) Ping(ctx context.Context, name string) (mcpclient.Info, error) {
	s, err := h.lookup(name)
	if err != nil {
		return mcpclient.Info{}, err
	}
	c, err := h.freshClient(ctx, s)
	if err != nil {
		h.fail(s, err)
		return mcpclient.Info{}, err
	}
	info, err := c.Initialize(ctx)
	h.mu.Lock()
	if err != nil {
		s.state, s.err = stateDown, err
		h.mu.Unlock()
		closeClient(c)
		if moved := mcpclient.MovedTo(err); moved != "" {
			err = fmt.Errorf("%w%s", err, movedNote(moved))
		}
		return mcpclient.Info{}, err
	}
	old := s.replaceClient(c)
	s.info, s.hasInfo, s.err = info, true, nil
	if s.state != stateUp {
		s.state = stateUp
	}
	h.mu.Unlock()
	closeClient(old)
	return info, nil
}

// lookup — сервер по имени; выключенный — ошибка с подсказкой.
func (h *Hub) lookup(name string) (*server, error) {
	s := h.server(name)
	if s == nil {
		names := h.Names()
		if len(names) == 0 {
			return nil, fmt.Errorf("сервер MCP %q не описан: серверов в конфиге нет ([mcp.servers.<имя>])", name)
		}
		return nil, fmt.Errorf("сервер MCP %q не описан (есть: %s)", name, strings.Join(names, ", "))
	}
	if !s.cfg.On() {
		return nil, fmt.Errorf("сервер MCP %s выключен (enabled = false)", name)
	}
	return s, nil
}

func (h *Hub) server(name string) *server {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.servers[name]
}

// freshClient — новый клиент со свежим ключом: ключ мог смениться
// (token_cmd), а прежний сеанс — пропасть вместе с сервером.
func (h *Hub) freshClient(ctx context.Context, s *server) (*mcpclient.Client, error) {
	token, err := s.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("сервер %s: ключ доступа: %w", s.name, err)
	}
	return mcpclient.New(mcpclient.Opts{Server: s.name, URL: s.cfg.URL, Token: token,
		Timeout: s.cfg.TimeoutDuration(), Version: h.version})
}

// connect — рукопожатие и список инструментов; итог — в состояние сервера
// и в реестр. Ошибка убирает инструменты сервера из реестра: звать их некуда.
func (h *Hub) connect(ctx context.Context, s *server) error {
	failed := func(err error) error {
		// Общий срок подключения вышел раньше срока обмена сервера: сказать
		// это прямо, а не «вызов прерван».
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("сервер %s не ответил за отведённый срок подключения: %w", s.name, err)
		}
		h.fail(s, err)
		return err
	}
	c, err := h.freshClient(ctx, s)
	if err != nil {
		return failed(err)
	}
	info, err := c.Initialize(ctx)
	if err != nil {
		closeClient(c) // сеанс мог успеть начаться — не бросать его серверу
		return failed(err)
	}
	list, err := c.ListTools(ctx)
	if err != nil {
		closeClient(c)
		return failed(err)
	}
	h.mu.Lock()
	old := s.replaceClient(c)
	s.info, s.hasInfo, s.err, s.state = info, true, nil, stateUp
	h.apply(s, list)
	h.mu.Unlock()
	err = h.publish(s.name)
	closeClient(old)
	return err
}

// replaceClient ставит серверу новый клиент и отдаёт прежний, если его пора
// закрыть сейчас: им никто не пользуется. Занятый прежний закроет последний
// его вызов (release). Под h.mu.
func (s *server) replaceClient(c *mcpclient.Client) (idle *mcpclient.Client) {
	old := s.client
	s.client = c
	if old != nil && old != c && s.busy[old] == 0 {
		return old
	}
	return nil
}

// use — клиент сервера для вызова, отмеченный занятым; nil — не подключён.
// Каждому use — свой release.
func (h *Hub) use(s *server) *mcpclient.Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := s.client
	if c != nil {
		s.busy[c]++
	}
	return c
}

// release — вызов клиентом c кончился. Клиент, которого переподключение уже
// сменило, закрывается последним вызовом — в стороне: вызов человека не ждёт
// чужого DELETE.
func (h *Hub) release(s *server, c *mcpclient.Client) {
	h.mu.Lock()
	s.busy[c]--
	idle := s.busy[c] <= 0
	if idle {
		delete(s.busy, c)
	}
	retired := idle && c != s.client
	h.mu.Unlock()
	if retired {
		go closeClient(c)
	}
}

// closeClient заканчивает сеанс клиента (DELETE) за closeTimeout; ошибка не
// важна — сервер, который не ответил, сеанс забудет сам.
func closeClient(c *mcpclient.Client) {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	_ = c.Close(ctx)
}

// Close заканчивает сеансы серверов (DELETE) — на выходе из программы, чтобы
// сервер не держал брошенные сеансы до своего срока. Клиент, которым сейчас
// идёт вызов, не трогается: DELETE посреди вызова заставил бы его послать
// вызов второй раз. Клиентами после Close можно пользоваться: следующий вызов
// поздоровается заново.
func (h *Hub) Close(ctx context.Context) error {
	h.mu.Lock()
	var idle []*mcpclient.Client
	for _, name := range h.names {
		if s := h.servers[name]; s.client != nil && s.busy[s.client] == 0 {
			idle = append(idle, s.client)
		}
	}
	h.mu.Unlock()
	var errs []error
	for _, c := range idle {
		if err := c.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// refresh перечитывает список инструментов после уведомления сервера
// (Stale) и снимает отметку «перечитывается» (pending, refreshAfterCall) под
// тем же замком, что кладёт новый список: вызов, собранный после, видит либо
// отметку, либо новый список. Неудача списка не трогает: инструменты
// остаются прежними, а ошибка видна в /mcp.
func (h *Hub) refresh(ctx context.Context, s *server, c *mcpclient.Client) {
	list, err := c.ListTools(ctx)
	h.mu.Lock()
	s.pending--
	if err != nil {
		s.err = fmt.Errorf("перечитать список инструментов не вышло: %w", err)
		h.mu.Unlock()
		return
	}
	s.err = nil
	h.apply(s, list)
	h.mu.Unlock()
	_ = h.publish(s.name)
}

// fail — сервер не ответил или отказал: ошибка в состояние, инструменты
// из реестра долой (publish).
func (h *Hub) fail(s *server, err error) {
	h.mu.Lock()
	s.state, s.err, s.tools = stateDown, err, nil
	h.mu.Unlock()
	_ = h.publish(s.name)
}

// apply принимает список инструментов сервера: отбор по tools, проверка имён
// и схем, повторы имён, бюджет сервера, отпечатки для Б5. Под h.mu.
func (h *Hub) apply(s *server, list []mcpclient.Tool) {
	s.tools, s.skipped, s.listed = nil, nil, time.Now()
	offered := map[string]bool{}
	named := map[string]bool{} // имена, уже встреченные в списке
	specs := 0                 // байт описаний и схем уже принятых инструментов
	for _, t := range list {
		offered[t.Name] = true
		if s.visible != nil && !s.visible[t.Name] {
			continue
		}
		if s.library && s.visible != nil && builtinTools[t.Name] {
			s.skipped = append(s.skipped, fmt.Sprintf("%s — встроенный инструмент: в режиме библиотеки "+
				"модель зовёт его под своим именем, вторым набором не отдаётся", t.Name))
			continue
		}
		if !validToolName(t.Name) {
			s.skipped = append(s.skipped, fmt.Sprintf("%q — недопустимое имя (латиница, цифры, _ - ., до 128 знаков)", clip(t.Name, 60)))
			continue
		}
		// Повтор имени: реестр отверг бы набор сервера целиком (до 23.10.2026
		// так и было — аудит, находка 3). Принят первый; повтор — в отброшенные,
		// даже если первый сам не прошёл: какой из двух исполнит сервер, знает
		// только он.
		if named[t.Name] {
			s.skipped = append(s.skipped, fmt.Sprintf("%s — имя повторяется в списке сервера: повтор отброшен, в счёт идёт первый", t.Name))
			continue
		}
		named[t.Name] = true
		if len(t.InputSchema) > maxSchemaBytes {
			s.skipped = append(s.skipped, fmt.Sprintf("%s — схема параметров %d байт, предел %d", t.Name, len(t.InputSchema), maxSchemaBytes))
			continue
		}
		schema, err := cleanSchema(t.InputSchema)
		if err != nil {
			schema = t.InputSchema // не JSON — отказ с причиной даст RawParams
		}
		params, err := ollama.RawParams(schema)
		if err != nil {
			s.skipped = append(s.skipped, fmt.Sprintf("%s — %v", t.Name, err))
			continue
		}
		if len(s.tools) >= maxServerTools {
			s.skipped = append(s.skipped, fmt.Sprintf("%s — у сервера уже %d инструментов, больше модели не отдаётся; "+
				"нужные перечислите в tools", t.Name, maxServerTools))
			continue
		}
		desc := describe(s.name, t.Description)
		cost := len(desc) + len(params.Raw)
		if specs+cost > maxServerSpecs {
			s.skipped = append(s.skipped, fmt.Sprintf("%s — описания и схемы инструментов сервера вместе больше %d байт "+
				"(уже принято %d, у этого %d); нужные перечислите в tools", t.Name, maxServerSpecs, specs, cost))
			continue
		}
		specs += cost
		fp := fingerprint(t)
		if prev, seen := s.first[t.Name]; !seen {
			s.first[t.Name] = fp
		} else if prev != fp {
			s.changed[t.Name] = true
		}
		s.tools = append(s.tools, entry{
			remote: t.Name,
			name:   tools.ExternalName(s.name, t.Name),
			desc:   desc,
			params: params,
		})
	}
	for _, want := range s.cfg.Tools {
		if !offered[want] {
			s.skipped = append(s.skipped, fmt.Sprintf("%s — есть в tools, а у сервера такого инструмента нет", want))
		}
	}
}

// publish кладёт инструменты сервера в реестр (или убирает их оттуда).
// Служба библиотеки — только перечисленные в tools без дублей встроенных
// (exposed, apply).
//
// Снимок и SetExternal идут под одним замком публикации (pubMu): при обрыве
// и подключении одного сервера разом опоздавший старый снимок иначе ложился
// поверх нового. Отказ реестра не глотается: сервер переходит в «ошибку» с
// причиной, видной в /mcp, а его инструменты у модели убираются целиком. До
// 23.10.2026 отказ глотали Connect, refresh и fail, и /mcp писал «подключён,
// инструментов N», когда у модели не было ни одного (аудит, находка 3).
func (h *Hub) publish(name string) error {
	h.pubMu.Lock()
	defer h.pubMu.Unlock()
	h.mu.Lock()
	reg, s := h.reg, h.servers[name]
	if reg == nil || s == nil {
		h.mu.Unlock()
		return nil
	}
	var list []tools.Tool
	if s.exposed() && s.state == stateUp {
		for _, e := range s.tools {
			list = append(list, &extTool{hub: h, srv: s, e: e})
		}
	}
	h.mu.Unlock()
	err := reg.SetExternal(name, list)
	if err == nil {
		return nil
	}
	err = fmt.Errorf("инструменты сервера не переданы модели: %w", err)
	h.mu.Lock()
	s.state, s.err, s.tools = stateDown, err, nil
	h.mu.Unlock()
	_ = reg.SetExternal(name, nil) // пустой набор реестр не отвергает
	return err
}

// validToolName — имя инструмента сервера годится в mcp__<сервер>__<имя>:
// модели его называть, а правилу Fetch(mcp://…) сравнивать без шаблонов.
func validToolName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// fingerprint — отпечаток описания и схемы. Схема приводится к одному виду
// (ключи по алфавиту), чтобы перестановка полей сервером изменением не считалась.
func fingerprint(t mcpclient.Tool) string {
	var v any
	schema := []byte(t.InputSchema)
	if json.Unmarshal(t.InputSchema, &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			schema = b
		}
	}
	sum := sha256.Sum256(append(append([]byte(t.Description), 0), schema...))
	return hex.EncodeToString(sum[:])
}

// describe — описание для модели: пометка сервера, без управляющих и
// невидимых знаков (перевод строки — пробел), не длиннее maxDescription.
// Текст чужой: пометка говорит модели, откуда он, а предел не даёт серверу
// занять контекст своим «описанием».
func describe(server, desc string) string {
	text := cleanText(desc)
	if text == "" {
		text = "(сервер не описал инструмент)"
	}
	return clip("[внешний сервер "+server+"] "+text, maxDescription)
}

// cleanText — чужой текст без управляющих и невидимых знаков: Cc, Cf,
// селекторы вариантов (U+FE00–FE0F, U+E0100–E01EF — ими прячут текст в
// «пустом» месте) и знаки Mn без базы, то есть в начале или после пробела;
// Mn после буквы (и + U+0306) — часть буквы и остаётся. Пробелы, переводы
// строк и табуляции — одним пробелом, по краям — ни одного.
func cleanText(s string) string {
	var b strings.Builder
	space := false
	base := false // последний записанный знак — видимый, к нему приложится Mn
	for _, r := range s {
		switch {
		case unicode.IsSpace(r): // и перевод строки, и табуляция
			space = b.Len() > 0
			base = false
			continue
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r):
			continue
		case unicode.Is(unicode.Mn, r) && !base:
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
		base = true
	}
	return b.String()
}

// cleanSchema чистит схему тем же правилом, что describe: всё, что модель
// читает словами, на любой глубине — описания, title, имена свойств и
// required, строки внутри items, anyOf и прочих слов схемы. Схема уходит
// модели целиком, и невидимый знак в имени параметра читается ею так же, как
// в описании инструмента; до 23.10.2026 чистились одни description (аудит).
//
// Порядок ключей и всё прочее остаются как прислал сервер; значения enum,
// const, default и examples не трогаются — это данные, которые модель
// должна вернуть слово в слово. Чистить нечего — схема та же, байт в байт.
// Ошибка — не JSON; такую отвергнет RawParams.
func cleanSchema(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	w := schemaRewriter{dec: dec}
	if err := w.value(false, false); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("после схемы лишний текст")
	}
	if !w.changed {
		return raw, nil
	}
	return json.RawMessage(w.out.Bytes()), nil
}

// schemaRewriter переписывает JSON по лексемам, сохраняя порядок ключей.
type schemaRewriter struct {
	dec     *json.Decoder
	out     bytes.Buffer
	changed bool
}

// value переписывает одно значение. data — внутри enum/const/default/examples,
// где не чистится ничего; вне данных чистится каждая строка и каждый ключ.
// names — объект, чьи ключи — имена (properties, $defs…), а не слова схемы:
// свойство по имени default или description — такая же схема, как прочие.
func (w *schemaRewriter) value(data, names bool) error {
	tok, err := w.dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			w.out.WriteByte('{')
			for i := 0; w.dec.More(); i++ {
				key, err := w.dec.Token()
				if err != nil {
					return err
				}
				name, _ := key.(string)
				if !data {
					name = w.clean(name)
				}
				if i > 0 {
					w.out.WriteByte(',')
				}
				w.str(name)
				w.out.WriteByte(':')
				childData, childNames := data, false
				if !data && !names {
					switch name {
					case "enum", "const", "default", "examples":
						childData = true
					case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
						childNames = true
					}
				}
				if err := w.value(childData, childNames); err != nil {
					return err
				}
			}
			w.out.WriteByte('}')
		case '[':
			w.out.WriteByte('[')
			for i := 0; w.dec.More(); i++ {
				if i > 0 {
					w.out.WriteByte(',')
				}
				if err := w.value(data, false); err != nil {
					return err
				}
			}
			w.out.WriteByte(']')
		}
		_, err := w.dec.Token() // закрывающая скобка
		return err
	case string:
		if !data {
			v = w.clean(v)
		}
		w.str(v)
	case json.Number:
		w.out.WriteString(v.String())
	case bool:
		w.out.WriteString(strconv.FormatBool(v))
	case nil:
		w.out.WriteString("null")
	}
	return nil
}

// clean — строка схемы по правилу cleanText; изменение отмечается.
func (w *schemaRewriter) clean(s string) string {
	if c := cleanText(s); c != s {
		w.changed = true
		return c
	}
	return s
}

// str пишет строку JSON без замены <, > и & на <: схема — не HTML.
func (w *schemaRewriter) str(s string) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	w.out.Write(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
}

// clip — не длиннее n знаков, с многоточием.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// CleanURL — адрес без строки запроса, якоря и учётных данных: в строке
// запроса бывает ключ, а адрес показывается человеку и пишется в журналы.
func CleanURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "(адрес не разобрался)"
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// libraryURL — вход MCP службы библиотеки: <kb.server_url>/mcp. /mcp
// дописывается к пути, а не в конец строки: у адреса с якорем вызов иначе
// ушёл бы в корень службы, у адреса со строкой запроса — по кривому пути.
// Строка запроса и учётные данные остаются как есть: клиент MCP на них
// отказывает с понятной причиной (ключ в адресе не поддерживается).
func libraryURL(serverURL string) string {
	u, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil {
		return strings.TrimRight(strings.TrimSpace(serverURL), "/") + "/mcp" // отказ даст клиент
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/mcp"
	u.RawPath = ""
	return u.String()
}

// sameURL — один и тот же вход: схема и машина без учёта регистра, порт по
// умолчанию — что явный, что подразумеваемый (80 у http, 443 у https), путь
// без конечной косой черты; строка запроса и учётные данные не в счёт. Так
// служба библиотеки узнаётся и записанная в [mcp.servers] иначе, чем в
// kb.server_url, и не уходит модели вторым набором (аудит 23.10.2026). Имя
// машины и её адрес — разные входы: сравнить их можно только спросив DNS.
func sameURL(a, b string) bool {
	keyA, okA := urlKey(a)
	keyB, okB := urlKey(b)
	return okA && okB && keyA == keyB
}

// urlKey — адрес в одном виде для sameURL; false — не разобрался или без машины.
func urlKey(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	host := net.JoinHostPort(strings.ToLower(u.Hostname()), port)
	return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/"), true
}

// errNotConnected — у сервера нет живого клиента.
var errNotConnected = errors.New("сервер не подключён")
