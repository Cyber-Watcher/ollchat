package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Ресурсы и промпты службы (этап 112, С12). «Resources give models access to
// static or dynamic data with minimal parsing or hardcoded logic … Prompts in
// MCP are reusable prompt templates that can guide the model through specific
// workflows or tasks» («AI Agents in Practice 2026», стр. 223). До 16.10.2026
// служба отдавала только инструменты.
//
// Ресурс — кусок библиотеки по id из выдачи search: kb://books/167/1 — то же,
// что kb_read {"id":"books/167#1"}, и идёт тем же путём, что вызов (политика,
// выключатель disabled_tools, журнал шагов). «#» в адресе не годится: клиент
// вправе отрезать его как фрагмент, поэтому номер куска — последний сегмент
// пути. Перечислять сотни тысяч кусков списком незачем: список отдаёт один
// ресурс — состояние библиотеки, — а куски описаны шаблоном адреса.
//
// Промпты — типовые сценарии поиска, которые иначе пишутся в каждое задание:
// библиотека (search → kb_read → graph_*) и коллекция документов.

const (
	resourceScheme = "kb://"
	statusURI      = resourceScheme + "status"
	statusTool     = "kb_status" // инструмент самой службы ollmcp, вне реестра ollchat

	// codeResourceNotFound — ошибка MCP «ресурс не найден».
	codeResourceNotFound = -32002
)

// chunkURI — адрес ресурса для id куска: "books/167#1" → "kb://books/167/1".
func chunkURI(id string) string {
	return resourceScheme + strings.Replace(id, "#", "/", 1)
}

// chunkID — id куска по адресу ресурса; false — адрес не про кусок.
func chunkID(uri string) (string, bool) {
	rest, ok := strings.CutPrefix(uri, resourceScheme)
	if !ok {
		return "", false
	}
	i := strings.LastIndex(rest, "/")
	if i <= 0 || i == len(rest)-1 || !strings.Contains(rest[:i], "/") {
		return "", false
	}
	// Пустой сегмент головы — не адрес куска: `kb:////0` давал id `/#0`,
	// `kb:///a/1` — `/a#1` (пустая коллекция уходила в kb_read вместо отказа
	// «нет такого ресурса»; найдено фаззингом 17.10.2026, FuzzChunkID).
	for _, seg := range strings.Split(rest[:i], "/") {
		if seg == "" {
			return "", false
		}
	}
	return rest[:i] + "#" + rest[i+1:], true
}

// offers — отдаёт ли служба инструмент сейчас: есть в наборе и не выключен.
func (s *Server) offers(name string) bool {
	if s.Policy.Disabled[name] {
		return false
	}
	for _, n := range s.toolNames() {
		if n == name {
			return true
		}
	}
	return false
}

func (s *Server) resourcesList() map[string]any {
	res := []map[string]any{}
	if s.offers(statusTool) {
		res = append(res, map[string]any{"uri": statusURI, "name": statusTool, "mimeType": "text/plain",
			"description": "состояние библиотеки: книги, куски, векторы, граф понятий"})
	}
	return map[string]any{"resources": res}
}

func (s *Server) resourceTemplates() map[string]any {
	tpl := []map[string]any{}
	if s.offers(tools.NameKBRead) {
		tpl = append(tpl, map[string]any{
			"uriTemplate": resourceScheme + "{collection}/{doc}/{chunk}",
			"name":        "кусок библиотеки",
			"mimeType":    "text/plain",
			"description": "кусок по id из выдачи search: books/167#1 — это " + chunkURI("books/167#1"),
		})
	}
	return map[string]any{"resourceTemplates": tpl}
}

// resourceRead читает ресурс через вызов инструмента — тем же путём, что
// tools/call: выключенный kb_read не читается и ресурсом.
func (s *Server) resourceRead(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.URI == "" {
		return nil, &rpcError{codeInvalidParams, "не указан адрес ресурса (uri)"}
	}
	name, args := statusTool, map[string]any{}
	if p.URI != statusURI {
		id, ok := chunkID(p.URI)
		if !ok {
			return nil, &rpcError{codeResourceNotFound, "нет такого ресурса: " + p.URI +
				" (есть " + statusURI + " и куски вида " + chunkURI("books/167#1") + ")"}
		}
		name, args = tools.NameKBRead, map[string]any{"id": id}
	}
	raw, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	res, rerr := s.call(ctx, raw)
	if rerr != nil {
		return nil, rerr
	}
	if isErrorResult(res) {
		return nil, &rpcError{codeResourceNotFound, resultText(res)}
	}
	return map[string]any{"contents": []map[string]any{
		{"uri": p.URI, "mimeType": "text/plain", "text": resultText(res)},
	}}, nil
}

// promptSpec — один промпт: имя, описание, аргументы и текст.
type promptSpec struct {
	name, description string
	args              []promptArg
	render            func(a map[string]string) string
}

type promptArg struct {
	name, description string
	required          bool
}

var promptSpecs = []promptSpec{
	{
		name:        "search_library",
		description: "Найти ответ в библиотеке книг всеми путями: выдержки, граф понятий, чтение вокруг выдержки",
		args:        []promptArg{{"question", "вопрос своими словами", true}},
		render: func(a map[string]string) string {
			return "Вопрос: " + a["question"] + "\n\n" +
				"Ищи в библиотеке так:\n" +
				"1. search(query: вопрос своими словами) — выдержки из книг и понятия графа одной выдачей.\n" +
				"2. kb_read(id: id выдержки, around: 1) — если выдержка оборвана или нужен соседний текст.\n" +
				"3. graph_entity — что известно о понятии; graph_path — как связаны два понятия; " +
				"graph_topic — тема целиком.\n" +
				"Отвечай своими словами со ссылкой на книгу и страницу. Если выдача говорит, что в книгах " +
				"этого нет, — так и скажи и не приписывай книгам того, чего в них нет."
		},
	},
	{
		name:        "search_docs",
		description: "Найти ответ в одной коллекции документов (документация, код): поиск первым, файл — по номеру строки",
		args: []promptArg{{"question", "вопрос своими словами", true},
			{"collection", "имя коллекции, по которой искать", true}},
		render: func(a map[string]string) string {
			return "Вопрос: " + a["question"] + "\n\n" +
				"Ищи так:\n" +
				"1. search(query: вопрос своими словами, collection: \"" + a["collection"] + "\") — первым делом, " +
				"до чтения файлов.\n" +
				"2. Файл открывай по номеру строки из выдачи и кусками, а не целиком.\n" +
				"3. Ответа нет — новый поиск другими словами, а не перебор файлов.\n" +
				"В ответе называй файл и строку, откуда взято."
		},
	},
}

func promptsList() map[string]any {
	out := make([]map[string]any, 0, len(promptSpecs))
	for _, p := range promptSpecs {
		args := make([]map[string]any, 0, len(p.args))
		for _, a := range p.args {
			args = append(args, map[string]any{"name": a.name, "description": a.description, "required": a.required})
		}
		out = append(out, map[string]any{"name": p.name, "description": p.description, "arguments": args})
	}
	return map[string]any{"prompts": out}
}

func promptGet(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{codeInvalidParams, "не разобраны параметры: " + err.Error()}
	}
	for _, sp := range promptSpecs {
		if sp.name != p.Name {
			continue
		}
		for _, a := range sp.args {
			if a.required && strings.TrimSpace(p.Arguments[a.name]) == "" {
				return nil, &rpcError{codeInvalidParams, fmt.Sprintf("промпт %s: не задан аргумент %s", sp.name, a.name)}
			}
		}
		return map[string]any{
			"description": sp.description,
			"messages": []map[string]any{{"role": "user",
				"content": map[string]any{"type": "text", "text": sp.render(p.Arguments)}}},
		}, nil
	}
	return nil, &rpcError{codeInvalidParams, "нет такого промпта: " + p.Name}
}
