package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Протокол MCP поверх JSON-RPC 2.0.
//
// Своя реализация, а не библиотека. Причина простая: протокол здесь — это шесть
// методов и один формат сообщения, а всякая внешняя зависимость в проекте,
// живущем на стандартной библиотеке плюс три пакета, стоит дороже, чем эти
// двести строк. Заодно видно, что именно уходит клиенту, — при разборе чужого
// поведения это решает.

// protocolVersion — версия протокола, о которой договариваемся с клиентом.
const protocolVersion = "2024-11-05"

// serverName и serverVersion представляются клиенту при подключении.
const (
	serverName    = "ollchat-kb"
	serverVersion = "0.1.0"
)

// rpcRequest — входящее сообщение JSON-RPC.
//
// Поле ID отсутствует у уведомлений: на них отвечать не нужно вовсе, и это
// не мелочь — лишний ответ на уведомление ломает разбор у клиента.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse — ответ JSON-RPC.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError — ошибка JSON-RPC.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Коды ошибок JSON-RPC, которые мы используем.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Server — сервер MCP поверх набора инструментов ollchat.
type Server struct {
	// Steps — журнал вызовов: имя инструмента, аргументы, исход, время.
	// nil — не писать. Служба смотрит наружу, и без этого журнала нельзя
	// сказать, кто и что у неё спрашивал.
	Steps *steplog.Writer
	// inFlight — сколько вызовов инструментов идёт сейчас (Ц4).
	inFlight atomic.Int64
	// Policy — пределы параметров, потолок ответа и срок вызова (policy.go).
	Policy Policy
	// WatchFiles — файлы настроек, правка которых перезапускает службу так же,
	// как подмена бинаря; Validate проверяет их до перезапуска, чтобы
	// испорченная правка не уронила службу у клиента (reexec_linux.go).
	WatchFiles []string
	Validate   func() error
	mu         sync.Mutex
	registry   *tools.Registry
	extra      []Tool // инструменты, которых нет в реестре ollchat: kb_status
	inited     bool
	// denied — отказы входа без ключа по адресам: когда записан последний
	// и сколько с тех пор пропущено (logDenied). Под mu.
	denied map[string]*deniedMark
}

// deniedMark — след отказов одного адреса между записями журнала.
type deniedMark struct {
	at      time.Time
	skipped int
}

// Пределы журнала отказов: запись на адрес не чаще раза в deniedEvery,
// адресов в памяти не больше deniedMaxHosts — поток отказов с разных адресов
// не раздует ни журнал, ни память.
const (
	deniedEvery    = time.Minute
	deniedMaxHosts = 1024
)

// Tool — инструмент, добавленный самим сервером поверх реестра ollchat.
type Tool struct {
	Spec ollama.ToolSpec
	Run  func(ctx context.Context, args map[string]any) (string, error)
}

// NewServer собирает сервер.
func NewServer(registry *tools.Registry, extra ...Tool) *Server {
	return &Server{registry: registry, extra: extra}
}

// nullID — id ответа, когда id запроса узнать не из чего. JSON-RPC 2.0
// требует в таком ответе "id": null: ответ вовсе без id клиент не может
// сопоставить ни с чем и молча отбрасывает, а ждущий запрос висит до таймаута.
var nullID = json.RawMessage("null")

// Handle обрабатывает одно сообщение и возвращает ответ.
//
// Пустой ответ означает уведомление — на него по протоколу отвечать нельзя.
func (s *Server) Handle(ctx context.Context, raw []byte) []byte {
	raw = bytes.TrimSpace(raw)
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		switch {
		case !json.Valid(raw):
			return must(rpcResponse{JSONRPC: "2.0", ID: nullID,
				Error: &rpcError{codeParse, "не разобрано: " + err.Error()}})
		case len(raw) > 0 && raw[0] == '[':
			return refuseBatch(raw)
		}
		// JSON цел, но это не запрос: строка, число или поле не того типа.
		return must(rpcResponse{JSONRPC: "2.0", ID: idOrNull(req.ID),
			Error: &rpcError{codeInvalidRequest, "не запрос JSON-RPC: " + err.Error()}})
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return must(rpcResponse{JSONRPC: "2.0", ID: idOrNull(req.ID),
			Error: &rpcError{codeInvalidRequest, "нужен jsonrpc 2.0"}})
	}
	// Уведомление: идентификатора нет, ответа быть не должно.
	if len(req.ID) == 0 {
		return nil
	}

	result, rerr := s.dispatch(ctx, req)
	if rerr != nil {
		return must(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rerr})
	}
	return must(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
}

// idOrNull — id запроса, а если его нет — null.
func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return nullID
	}
	return id
}

// refuseBatch отвечает на пакет запросов (JSON-массив).
//
// Пакетов служба не умеет, а MCP 2025-06-18 их и вовсе убрал. Прежде массив
// получал «не разобрано» (-32700), хотя разобран он был: клиент искал ошибку
// в своём JSON. Теперь каждый запрос пакета получает свой отказ -32600 со
// своим id — клиент сопоставит его с ожидающим вызовом; уведомлениям ответа
// нет, как и положено; на пустой пакет — один отказ с id null (JSON-RPC 2.0).
func refuseBatch(raw []byte) []byte {
	const why = "пакеты запросов не поддерживаются: шлите запросы по одному"
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return must(rpcResponse{JSONRPC: "2.0", ID: nullID,
			Error: &rpcError{codeInvalidRequest, "пустой пакет запросов"}})
	}
	out := make([]rpcResponse, 0, len(items))
	for _, it := range items {
		var r struct {
			ID json.RawMessage `json:"id"`
		}
		isObject := json.Unmarshal(it, &r) == nil
		if isObject && len(r.ID) == 0 {
			continue // уведомление: ответа нет и в пакете
		}
		out = append(out, rpcResponse{JSONRPC: "2.0", ID: idOrNull(r.ID),
			Error: &rpcError{codeInvalidRequest, why}})
	}
	if len(out) == 0 {
		return nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return must(rpcResponse{JSONRPC: "2.0", ID: nullID, Error: &rpcError{codeInvalidRequest, why}})
	}
	return b
}

// logInit пишет рукопожатие в журнал шагов: запрошенную клиентом версию
// протокола, отданную службой и имя клиента (этап 112, Ц1). Служба отвечает
// своей версией на любую просьбу; по этим записям видно, кто приходит и не
// пора ли поддержать ревизию без сеанса (2026-07-28), на сеансах держится
// уведомление о смене набора (этап 109).
func (s *Server) logInit(params json.RawMessage) {
	if s.Steps == nil {
		return
	}
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	_ = json.Unmarshal(params, &p) // кривые параметры — тоже запись, с пустыми полями
	s.Steps.Write(steplog.Step{Kind: steplog.KindInit, Outcome: steplog.OutcomeOK,
		Extra: map[string]any{"requested": p.ProtocolVersion, "answered": protocolVersion,
			"client": p.ClientInfo.Name, "client_version": p.ClientInfo.Version}})
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		s.markInited()
		s.logInit(req.Params)
		return map[string]any{
			"protocolVersion": protocolVersion,
			// listChanged обещает клиенту уведомление о смене набора (этап 109, А1).
			// Без этого флага клиент честно держал список, сложившийся при старте
			// сеанса: 26.09.2026 весь день звались kb_search и graph_search, которых
			// служба уже не отдавала.
			// Ресурсы и промпты — с 16.10.2026 (этап 112, С12, resources.go).
			"capabilities": map[string]any{"tools": map[string]any{"listChanged": true},
				"resources": map[string]any{}, "prompts": map[string]any{}},
			"serverInfo": map[string]any{"name": serverName, "version": serverVersion},
			"instructions": "Библиотека технических книг пользователя и граф понятий по ней. " +
				"Ищи в книгах перед тем, как отвечать по памяти: выдача содержит книгу и страницу, " +
				"и на них можно сослаться.",
		}, nil

	case "ping":
		return map[string]any{}, nil

	case "tools/list":
		return map[string]any{"tools": s.list()}, nil

	case "tools/call":
		return s.call(ctx, req.Params)

	case "resources/list":
		return s.resourcesList(), nil

	case "resources/templates/list":
		return s.resourceTemplates(), nil

	case "resources/read":
		return s.resourceRead(ctx, req.Params)

	case "prompts/list":
		return promptsList(), nil

	case "prompts/get":
		return promptGet(req.Params)

	default:
		return nil, &rpcError{codeMethodNotFound, "неизвестный метод: " + req.Method}
	}
}

// logDenied пишет в журнал шагов отказ входа без ключа (этап 112, С2). Других
// отказов у службы нет: инструменты не пишут на диск и команд не запускают
// (web_search ходит наружу, search с kb.translate зовёт модель — см.
// tools.ReadOnlyNames), поправка параметров —
// `clamped`, неизвестный инструмент — `invalid`. До 16.10.2026 этот отказ шёл
// лишь в stderr под -v, и подбор ключа в журнале виден не был. Тело запроса
// без ключа не разбирается: имени инструмента нет, и вид записи — `auth`, а не
// `tool` (иначе перепись вызовов считала бы отказ вызовом без имени).
func (s *Server) logDenied(remote string) {
	if s.Steps == nil {
		return
	}
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	now := time.Now()
	s.mu.Lock()
	if s.denied == nil || len(s.denied) >= deniedMaxHosts {
		s.denied = map[string]*deniedMark{}
	}
	m := s.denied[host]
	if m != nil && now.Sub(m.at) < deniedEvery {
		m.skipped++
		s.mu.Unlock()
		return
	}
	skipped := 0
	if m != nil {
		skipped = m.skipped
	}
	s.denied[host] = &deniedMark{at: now}
	s.mu.Unlock()
	s.Steps.Write(steplog.Step{Kind: steplog.KindAuth, Outcome: steplog.OutcomeDenied,
		Note: "нет ключа доступа", Extra: map[string]any{"remote": host, "skipped": skipped}})
}

func (s *Server) markInited() {
	s.mu.Lock()
	s.inited = true
	s.mu.Unlock()
}

// toolsChanged — уведомление «набор инструментов сменился, перечитай».
// Сам набор внутри одного процесса не меняется: он меняется вместе с бинарём,
// поэтому шлют его транспорты — HTTP после перезапуска службы, stdio после
// подмены бинаря (transport.go, reexec_linux.go).
var toolsChanged = []byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)

// Fingerprint — отпечаток набора инструментов: имена, описания и схемы.
// Совпал отпечаток — клиенту перечитывать нечего, даже если служба
// перезапускалась.
func (s *Server) Fingerprint() string {
	b, _ := json.Marshal(s.list())
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}

// mcpTool — описание инструмента в терминах MCP.
type mcpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

// list собирает список инструментов.
// Tools — что служба отдаёт клиенту. Публично: список печатает `ollmcp --list`.
func (s *Server) Tools() []mcpTool {
	return s.list()
}

func (s *Server) list() []mcpTool {
	var out []mcpTool
	if s.registry != nil {
		for _, spec := range s.registry.Specs() {
			if s.Policy.Disabled[spec.Function.Name] {
				continue
			}
			out = append(out, mcpTool{
				Name:        spec.Function.Name,
				Description: spec.Function.Description,
				InputSchema: s.schemaFor(spec.Function.Name, spec.Function.Parameters),
			})
		}
	}
	for _, t := range s.extra {
		if s.Policy.Disabled[t.Spec.Name] {
			continue
		}
		out = append(out, mcpTool{
			Name:        t.Spec.Name,
			Description: t.Spec.Description,
			InputSchema: s.schemaFor(t.Spec.Name, t.Spec.Parameters),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// schemaFor — схема с диапазонами, переписанными по пределам службы.
func (s *Server) schemaFor(tool string, p ollama.ToolParams) map[string]any {
	if len(s.Policy.Limits[tool]) > 0 {
		props := make(map[string]ollama.ToolProp, len(p.Properties))
		for name, prop := range p.Properties {
			if l, ok := s.Policy.limitOf(tool, name); ok {
				prop.Description = describe(prop.Description, l)
			}
			props[name] = prop
		}
		p.Properties = props
	}
	return schemaOf(p)
}

// schemaOf переводит описание параметров в JSON Schema, как её ждёт MCP.
func schemaOf(p ollama.ToolParams) map[string]any {
	props := map[string]any{}
	for name, prop := range p.Properties {
		item := map[string]any{"type": prop.Type}
		if prop.Description != "" {
			item["description"] = prop.Description
		}
		if len(prop.Enum) > 0 {
			item["enum"] = prop.Enum
		}
		props[name] = item
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(p.Required) > 0 {
		schema["required"] = p.Required
	}
	return schema
}

// callParams — тело вызова инструмента.
type callParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	// Meta — `_meta` запроса: что клиент сообщает о вызове помимо аргументов
	// (номер вызова у себя, метка прогресса). В журнал — clientMeta.
	Meta map[string]any `json:"_meta"`
}

// call выполняет инструмент.
//
// Ошибка самого инструмента возвращается не как ошибка протокола, а как ответ
// с признаком isError: так велит MCP, и это правильно — клиент должен показать
// её модели, а не оборвать соединение.
func (s *Server) call(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	started := time.Now()
	// Работа «в полёте» (этап 112, Ц4): сколько вызовов идёт одновременно с этим,
	// включая его самого. Предела у входа MCP нет; сперва число, потом решение.
	inFlight := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	// Журнал — с контекстом: поиск под инструментом пишет свою запись `search`
	// с оценкой первого места (этап 112, Ч17). Хода у клиента MCP нет.
	res, rerr := s.callInner(steplog.NewContext(ctx, s.Steps, ""), raw)
	if s.Steps != nil {
		var p callParams
		_ = json.Unmarshal(raw, &p)
		args, _ := json.Marshal(p.Arguments)
		outcome := steplog.OutcomeOK
		switch {
		case rerr != nil:
			outcome = steplog.OutcomeInvalid
		case s.Policy.Disabled[p.Name]:
			outcome = steplog.OutcomeDenied
		case isErrorResult(res):
			outcome = steplog.OutcomeFailed
		}
		extra := callExtra(res, inFlight)
		if m := clientMeta(p.Meta); m != nil {
			extra["client_meta"] = m
		}
		s.Steps.Write(steplog.Step{Kind: steplog.KindTool, Tool: p.Name, Args: string(args),
			Outcome: outcome, MS: time.Since(started).Milliseconds(), Extra: extra})
	}
	return res, rerr
}

// callExtra — что «ok» скрывает (этап 112, Ц3: «свести всё в один бит „успех“ —
// значит стереть различия»): размер ответа, пустой ли он, обрезан ли по потолку,
// поправила ли служба параметры; и сколько вызовов шло одновременно (Ц4).
// Признаки берутся по меткам, которые ставят сами callResult, tools.truncate и
// Policy.finish, — не по догадке о тексте. Метки ищутся только в хвосте и в том
// порядке, в каком их ставит Policy.finish: сперва приписка службы, затем
// tools.Truncate; обрезка внутри самого инструмента стоит перед припиской.
func callExtra(res any, inFlight int64) map[string]any {
	text := resultText(res)
	body, truncated := tailNote(text, "[вывод обрезан: ")
	body, clamped := tailNote(body, "[служба: ")
	if !truncated {
		_, truncated = tailNote(body, "[вывод обрезан: ")
	}
	return map[string]any{
		"bytes":     len(text),
		"empty":     text == "(пусто)",
		"truncated": truncated,
		"clamped":   clamped,
		"in_flight": inFlight,
	}
}

// tailNote отрезает от text приписку "\n\n<prefix>…]", если она стоит в самом
// конце и занимает одну строку. В середине ответа такая строка — цитата (выдержка
// из кода или документации), а не признак: 16.10.2026 «обрезанным» записался
// поиск, в выдаче которого была строка этой самой функции.
func tailNote(text, prefix string) (string, bool) {
	i := strings.LastIndex(text, "\n\n"+prefix)
	if i < 0 || !strings.HasSuffix(text, "]") || strings.Contains(text[i+2:], "\n") {
		return text, false
	}
	return text[:i], true
}

// resultText — текст ответа инструмента (первая часть content).
func resultText(res any) string {
	m, ok := res.(map[string]any)
	if !ok {
		return ""
	}
	parts, _ := m["content"].([]any)
	if len(parts) == 0 {
		return ""
	}
	part, _ := parts[0].(map[string]any)
	text, _ := part["text"].(string)
	return text
}

// isErrorResult — вернул ли инструмент ошибку (isError в ответе MCP).
func isErrorResult(res any) bool {
	m, ok := res.(map[string]any)
	if !ok {
		return false
	}
	v, _ := m["isError"].(bool)
	return v
}

// Пределы clientMeta: метаданные клиента — номера и метки, не тела.
const (
	metaMaxKeys  = 8
	metaMaxBytes = 120
)

// clientMeta — `_meta` вызова в виде, пригодном для журнала (этап 112, В1):
// по нему запись службы связывается с вызовом у клиента — своего номера хода
// у клиента MCP служба не знает. Берутся только строки, числа и признаки;
// вложенное помечается «{…}», длинная строка обрезается. Пусто — nil.
func clientMeta(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > metaMaxKeys {
		keys = keys[:metaMaxKeys]
	}
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		switch v := meta[k].(type) {
		case string:
			out[k] = clipUTF8(v, metaMaxBytes)
		case float64, bool:
			out[k] = v
		case nil:
			out[k] = nil
		default:
			out[k] = "{…}"
		}
	}
	return out
}

// clipUTF8 обрезает строку до n байт, не разрезая букву.
func clipUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func (s *Server) callInner(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var p callParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{codeInvalidParams, "не разобраны параметры вызова: " + err.Error()}
	}
	if p.Name == "" {
		return nil, &rpcError{codeInvalidParams, "не указано имя инструмента"}
	}
	if p.Arguments == nil {
		p.Arguments = map[string]any{}
	}
	// Выключенный инструмент (этап 112, Ц6) не исполняется, даже если клиент
	// помнит его по старому списку: выключатель держит вызов, а не только список.
	if s.Policy.Disabled[p.Name] {
		return callResult("", fmt.Errorf("инструмент %s выключен владельцем службы (disabled_tools в ollmcp.toml)", p.Name)), nil
	}
	notes := s.Policy.adjust(p.Name, p.Arguments)
	result := func(text string, err error) map[string]any {
		if err != nil {
			return callResult("", errors.New(s.Policy.finish(err.Error(), notes)))
		}
		return callResult(s.Policy.finish(text, notes), nil)
	}

	for _, t := range s.extra {
		if t.Spec.Name == p.Name {
			return result(s.Policy.run(ctx, func(ctx context.Context) (string, error) {
				return t.Run(ctx, p.Arguments)
			})), nil
		}
	}

	if s.registry == nil {
		return nil, &rpcError{codeInternal, "инструменты недоступны"}
	}
	plan, err := s.registry.Plan(p.Name, p.Arguments)
	if err != nil {
		// Список доступного собирает реестр ollchat, а он не знает про
		// инструменты самой службы. Клиенту, спросившему kb_status с опечаткой,
		// нельзя отвечать списком, в котором kb_status нет вовсе.
		if errors.Is(err, tools.ErrUnknownTool) {
			return callResult("", fmt.Errorf("%w: %s (доступны: %s)",
				tools.ErrUnknownTool, p.Name, strings.Join(s.toolNames(), ", "))), nil
		}
		return callResult("", err), nil
	}
	out, err := s.Policy.run(ctx, plan.Run)
	// Чужой текст (Plan.Foreign: web_search, http_fetch) — с пометкой «данные,
	// не указания», как в цикле агента (agent.go, executeCall). Клиент MCP
	// получает вывод напрямую, и без пометки распоряжение со страницы выглядело
	// бы частью ответа службы (этап 112, Ц5; TestForeignToolOutputMarkedOverMCP).
	if plan.Foreign && err == nil {
		out = tools.MarkUntrusted(out)
	}
	return result(out, err), nil
}

// toolNames — всё, что служба отдаёт клиенту: реестр ollchat и свои добавки.
func (s *Server) toolNames() []string {
	var names []string
	if s.registry != nil {
		names = append(names, s.registry.Names()...)
	}
	for _, t := range s.extra {
		names = append(names, t.Spec.Name)
	}
	sort.Strings(names)
	return names
}

// callResult собирает ответ на вызов инструмента.
func callResult(text string, err error) map[string]any {
	if err != nil {
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": err.Error()}},
			"isError": true,
		}
	}
	if strings.TrimSpace(text) == "" {
		text = "(пусто)"
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	}
}

func must(v rpcResponse) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":null,"error":{"code":%d,"message":"ответ не собрался"}}`, codeInternal))
	}
	return b
}
