package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"sort"
	"strings"
	"sync"
	"time"

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
	Steps    *steplog.Writer
	mu       sync.Mutex
	registry *tools.Registry
	extra    []Tool // инструменты, которых нет в реестре ollchat: kb_status
	inited   bool
}

// Tool — инструмент, добавленный самим сервером поверх реестра ollchat.
type Tool struct {
	Spec ollama.ToolSpec
	Run  func(ctx context.Context, args map[string]any) (string, error)
}

// NewServer собирает сервер.
func NewServer(registry *tools.Registry, extra ...Tool) *Server {
	return &Server{registry: registry, extra: extra}
}

// Handle обрабатывает одно сообщение и возвращает ответ.
//
// Пустой ответ означает уведомление — на него по протоколу отвечать нельзя.
func (s *Server) Handle(ctx context.Context, raw []byte) []byte {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return must(rpcResponse{JSONRPC: "2.0", Error: &rpcError{codeParse, "не разобрано: " + err.Error()}})
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return must(rpcResponse{JSONRPC: "2.0", ID: req.ID,
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

func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		s.mu.Lock()
		s.inited = true
		s.mu.Unlock()
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
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

	default:
		return nil, &rpcError{codeMethodNotFound, "неизвестный метод: " + req.Method}
	}
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
			out = append(out, mcpTool{
				Name:        spec.Function.Name,
				Description: spec.Function.Description,
				InputSchema: schemaOf(spec.Function.Parameters),
			})
		}
	}
	for _, t := range s.extra {
		out = append(out, mcpTool{
			Name:        t.Spec.Name,
			Description: t.Spec.Description,
			InputSchema: schemaOf(t.Spec.Parameters),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
}

// call выполняет инструмент.
//
// Ошибка самого инструмента возвращается не как ошибка протокола, а как ответ
// с признаком isError: так велит MCP, и это правильно — клиент должен показать
// её модели, а не оборвать соединение.
func (s *Server) call(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	started := time.Now()
	res, rerr := s.callInner(ctx, raw)
	if s.Steps != nil {
		var p callParams
		_ = json.Unmarshal(raw, &p)
		args, _ := json.Marshal(p.Arguments)
		outcome := steplog.OutcomeOK
		switch {
		case rerr != nil:
			outcome = steplog.OutcomeInvalid
		case isErrorResult(res):
			outcome = steplog.OutcomeFailed
		}
		s.Steps.Write(steplog.Step{Kind: steplog.KindTool, Tool: p.Name, Args: string(args),
			Outcome: outcome, MS: time.Since(started).Milliseconds()})
	}
	return res, rerr
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

	for _, t := range s.extra {
		if t.Spec.Name == p.Name {
			text, err := t.Run(ctx, p.Arguments)
			return callResult(text, err), nil
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
	text, err := plan.Run(ctx)
	return callResult(text, err), nil
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
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","error":{"code":%d,"message":"ответ не собрался"}}`, codeInternal))
	}
	return b
}
