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
	// maxServerSchemas — байт схем всех инструментов одного сервера вместе:
	// схемы уходят в каждый запрос к модели, и без общего предела сервер
	// с сотней инструментов по 32 КБ занял бы контекст до вопроса. Две схемы
	// предельного размера; что не влезло — в отброшенные с причиной.
	maxServerSchemas = 2 * maxSchemaBytes
)

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
		libURL = strings.TrimRight(u, "/") + "/mcp"
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
			s := newServer(LibraryServer, config.MCPServer{URL: libURL, Timeout: cfg.KB.ServerTimeout})
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
		state: stateNever, first: map[string]string{}, changed: map[string]bool{}}
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
// Список инструментов не читает.
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
	defer h.mu.Unlock()
	if err != nil {
		s.state, s.err = stateDown, err
		return mcpclient.Info{}, err
	}
	s.client, s.info, s.hasInfo, s.err = c, info, true, nil
	if s.state != stateUp {
		s.state = stateUp
	}
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
		return failed(err)
	}
	list, err := c.ListTools(ctx)
	if err != nil {
		return failed(err)
	}
	h.mu.Lock()
	s.client, s.info, s.hasInfo, s.err, s.state = c, info, true, nil, stateUp
	h.apply(s, list)
	h.mu.Unlock()
	return h.publish(s.name)
}

// refresh перечитывает список инструментов после уведомления сервера
// (Stale). Неудача списка не трогает: инструменты остаются прежними, а
// ошибка видна в /mcp.
func (h *Hub) refresh(ctx context.Context, s *server, c *mcpclient.Client) {
	list, err := c.ListTools(ctx)
	h.mu.Lock()
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
// из реестра долой.
func (h *Hub) fail(s *server, err error) {
	h.mu.Lock()
	s.state, s.err, s.tools = stateDown, err, nil
	h.mu.Unlock()
	_ = h.publish(s.name)
}

// apply принимает список инструментов сервера: отбор по tools, проверка имён
// и схем, отпечатки для Б5. Под h.mu.
func (h *Hub) apply(s *server, list []mcpclient.Tool) {
	s.tools, s.skipped, s.listed = nil, nil, time.Now()
	offered := map[string]bool{}
	schemas := 0 // байт схем уже принятых инструментов
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
		if schemas+len(params.Raw) > maxServerSchemas {
			s.skipped = append(s.skipped, fmt.Sprintf("%s — схемы инструментов сервера вместе больше %d байт "+
				"(уже принято %d, у этого %d); нужные перечислите в tools", t.Name, maxServerSchemas, schemas, len(params.Raw)))
			continue
		}
		schemas += len(params.Raw)
		fp := fingerprint(t)
		if prev, seen := s.first[t.Name]; !seen {
			s.first[t.Name] = fp
		} else if prev != fp {
			s.changed[t.Name] = true
		}
		s.tools = append(s.tools, entry{
			remote: t.Name,
			name:   tools.ExternalName(s.name, t.Name),
			desc:   describe(s.name, t.Description),
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
func (h *Hub) publish(name string) error {
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
	return reg.SetExternal(name, list)
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

// cleanText — чужой текст без управляющих и невидимых знаков (Cf): пробелы,
// переводы строк и табуляции — одним пробелом, по краям — ни одного.
func cleanText(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r): // и перевод строки, и табуляция
			space = b.Len() > 0
			continue
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// cleanSchema чистит описания параметров внутри схемы тем же правилом, что
// describe: строки под ключом description на любой глубине — properties,
// items, anyOf. Схема уходит модели целиком, и невидимый знак во вложенном
// описании читается ею так же, как в описании инструмента.
//
// Порядок ключей и всё прочее остаются как прислал сервер; значения enum,
// const, default и examples не трогаются — это данные, которые модель
// должна вернуть слово в слово. Чистить нечего — схема та же, байт в байт.
// Ошибка — не JSON; такую отвергнет RawParams.
func cleanSchema(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	w := schemaRewriter{dec: dec}
	if err := w.value(false, false, false); err != nil {
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

// value переписывает одно значение. desc — это строка описания, её чистить;
// data — внутри enum/const/default/examples, где не чистится ничего; names —
// объект, чьи ключи — имена (properties, $defs…), а не слова схемы: свойство
// по имени default или description — такая же схема, как прочие.
func (w *schemaRewriter) value(desc, data, names bool) error {
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
				if i > 0 {
					w.out.WriteByte(',')
				}
				w.str(name)
				w.out.WriteByte(':')
				childDesc, childData, childNames := false, data, false
				if !data && !names {
					switch name {
					case "description":
						childDesc = true
					case "enum", "const", "default", "examples":
						childData = true
					case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
						childNames = true
					}
				}
				if err := w.value(childDesc, childData, childNames); err != nil {
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
				if err := w.value(false, data, false); err != nil {
					return err
				}
			}
			w.out.WriteByte(']')
		}
		_, err := w.dec.Token() // закрывающая скобка
		return err
	case string:
		if desc {
			if c := cleanText(v); c != v {
				v, w.changed = c, true
			}
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

// sameURL — один и тот же вход: схема и машина без учёта регистра, путь без
// конечной косой черты.
func sameURL(a, b string) bool {
	ua, ea := url.Parse(strings.TrimSpace(a))
	ub, eb := url.Parse(strings.TrimSpace(b))
	if ea != nil || eb != nil {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host) &&
		strings.TrimRight(ua.EscapedPath(), "/") == strings.TrimRight(ub.EscapedPath(), "/")
}

// errNotConnected — у сервера нет живого клиента.
var errNotConnected = errors.New("сервер не подключён")
