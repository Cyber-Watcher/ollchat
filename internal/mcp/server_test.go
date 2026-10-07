package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

func server(tools ...Tool) *Server {
	return NewServer(nil, tools...)
}

func probe() Tool {
	return Tool{
		Spec: ollama.ToolSpec{
			Name:        "проба",
			Description: "инструмент для проверки",
			Parameters: ollama.ToolParams{
				Type:     "object",
				Required: []string{"что"},
				Properties: map[string]ollama.ToolProp{
					"что": {Type: "string", Description: "что искать"},
				},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			if args["что"] == "сломайся" {
				return "", errors.New("не вышло")
			}
			s, _ := args["что"].(string)
			return "нашлось: " + s, nil
		},
	}
}

func call(t *testing.T, s *Server, body string) map[string]any {
	t.Helper()
	resp := s.Handle(context.Background(), []byte(body))
	if resp == nil {
		t.Fatal("ответа нет, а он ожидался")
	}
	var d map[string]any
	if err := json.Unmarshal(resp, &d); err != nil {
		t.Fatalf("ответ не разобрался: %v (%s)", err, resp)
	}
	return d
}

// Рукопожатие.
func TestHandshake(t *testing.T) {
	d := call(t, server(), `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	r, ok := d["result"].(map[string]any)
	if !ok {
		t.Fatalf("ответ без результата: %v", d)
	}
	if r["protocolVersion"] != protocolVersion {
		t.Errorf("версия протокола = %v", r["protocolVersion"])
	}
	tools, ok := r["capabilities"].(map[string]any)["tools"].(map[string]any)
	if !ok {
		t.Fatal("сервер не объявил, что умеет инструменты")
	}
	// Без этого флага клиент не ждёт уведомлений и держит список до перезапуска
	// сеанса (этап 109, А1).
	if tools["listChanged"] != true {
		t.Errorf("listChanged не объявлен: %v", tools)
	}
}

// Отпечаток набора: один и тот же у одинаковых наборов, другой — у разных,
// в том числе когда сменилось одно описание.
func TestFingerprint(t *testing.T) {
	a, b := server(probe()).Fingerprint(), server(probe()).Fingerprint()
	if a == "" || a != b {
		t.Fatalf("отпечатки одного набора разные: %q и %q", a, b)
	}
	changed := probe()
	changed.Spec.Description += "!"
	if server(changed).Fingerprint() == a {
		t.Error("смена описания не изменила отпечаток")
	}
	if server().Fingerprint() == a {
		t.Error("пустой набор дал тот же отпечаток")
	}
}

// На уведомление отвечать нельзя: лишний ответ ломает разбор у клиента.
func TestNoReplyToNotification(t *testing.T) {
	if resp := server().Handle(context.Background(),
		[]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); resp != nil {
		t.Errorf("на уведомление пришёл ответ: %s", resp)
	}
}

// Список инструментов со схемой.
func TestToolListWithSchema(t *testing.T) {
	d := call(t, server(probe()), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	list := d["result"].(map[string]any)["tools"].([]any)
	if len(list) != 1 {
		t.Fatalf("инструментов = %d", len(list))
	}
	tool := list[0].(map[string]any)
	if tool["name"] != "проба" {
		t.Errorf("имя = %v", tool["name"])
	}
	schema := tool["inputSchema"].(map[string]any)
	if schema["type"] != "object" {
		t.Errorf("схема без типа объекта: %v", schema)
	}
	req := schema["required"].([]any)
	if len(req) != 1 || req[0] != "что" {
		t.Errorf("обязательные поля = %v", req)
	}
	props := schema["properties"].(map[string]any)["что"].(map[string]any)
	if props["type"] != "string" || props["description"] == "" {
		t.Errorf("описание параметра потеряно: %v", props)
	}
}

// Вызов инструмента.
func TestToolCall(t *testing.T) {
	d := call(t, server(probe()),
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"проба","arguments":{"что":"горутины"}}}`)
	r := d["result"].(map[string]any)
	if r["isError"] == true {
		t.Fatalf("вызов признан ошибочным: %v", r)
	}
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "горутины") {
		t.Errorf("ответ = %q", text)
	}
}

// Ошибка инструмента — это ответ с признаком isError, а не ошибка протокола:
// клиент должен показать её модели, а не оборвать разговор.
func TestToolErrorIsNotProtocolError(t *testing.T) {
	d := call(t, server(probe()),
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"проба","arguments":{"что":"сломайся"}}}`)
	if _, ok := d["error"]; ok {
		t.Fatalf("вернулась ошибка протокола: %v", d)
	}
	r := d["result"].(map[string]any)
	if r["isError"] != true {
		t.Errorf("признак ошибки не выставлен: %v", r)
	}
}

// Неизвестный метод.
func TestUnknownMethod(t *testing.T) {
	d := call(t, server(), `{"jsonrpc":"2.0","id":5,"method":"чегоизволите"}`)
	e, ok := d["error"].(map[string]any)
	if !ok {
		t.Fatalf("ошибки нет: %v", d)
	}
	if int(e["code"].(float64)) != codeMethodNotFound {
		t.Errorf("код ошибки = %v", e["code"])
	}
}

// Битое сообщение: ошибка разбора и "id": null — ответ без id клиент
// сопоставить не может и отбрасывает (JSON-RPC 2.0).
func TestBrokenMessage(t *testing.T) {
	d := call(t, server(), `{это не json`)
	e := d["error"].(map[string]any)
	if int(e["code"].(float64)) != codeParse {
		t.Errorf("код ошибки разбора = %v", e["code"])
	}
	if id, ok := d["id"]; !ok || id != nil {
		t.Errorf("в ответе на неразобранное нужен \"id\": null, а там %v (есть: %v)", id, ok)
	}
}

// Цельный JSON, который не запрос, — не ошибка разбора, а -32600 с id null.
func TestNotARequest(t *testing.T) {
	for _, body := range []string{`"строка"`, `42`, `[]`} {
		d := call(t, server(), body)
		e, _ := d["error"].(map[string]any)
		if e == nil || int(e["code"].(float64)) != codeInvalidRequest {
			t.Errorf("%s: ожидался -32600, пришло %v", body, d)
		}
		if id, ok := d["id"]; !ok || id != nil {
			t.Errorf("%s: нужен \"id\": null, а там %v", body, id)
		}
	}
}

// Пакет запросов: каждому запросу — свой отказ -32600 с его id, уведомлению
// — ничего. Прежде весь массив получал «не разобрано» (-32700), хотя
// разобран был, и клиент искал ошибку в своём JSON.
func TestBatchIsRefusedPerRequest(t *testing.T) {
	resp := server().Handle(context.Background(), []byte(`[
		{"jsonrpc":"2.0","id":1,"method":"ping"},
		{"jsonrpc":"2.0","method":"notifications/initialized"},
		{"jsonrpc":"2.0","id":"б","method":"tools/list"}]`))
	var list []map[string]any
	if err := json.Unmarshal(resp, &list); err != nil {
		t.Fatalf("на пакет ждали массив ответов: %v (%s)", err, resp)
	}
	if len(list) != 2 {
		t.Fatalf("ответов %d, ожидалось 2 (уведомлению ответа нет): %s", len(list), resp)
	}
	for i, want := range []any{float64(1), "б"} {
		e, _ := list[i]["error"].(map[string]any)
		if list[i]["id"] != want || e == nil || int(e["code"].(float64)) != codeInvalidRequest {
			t.Errorf("ответ %d: %v, ожидался отказ -32600 с id %v", i, list[i], want)
		}
	}
	if resp := server().Handle(context.Background(),
		[]byte(`[{"jsonrpc":"2.0","method":"notifications/initialized"}]`)); resp != nil {
		t.Errorf("на пакет из одних уведомлений ответа быть не должно: %s", resp)
	}
}

// Чужая версия протокола JSONRPC.
func TestForeignJSONRPCVersion(t *testing.T) {
	d := call(t, server(), `{"jsonrpc":"1.0","id":8,"method":"ping"}`)
	if _, ok := d["error"]; !ok {
		t.Errorf("принята чужая версия JSON-RPC: %v", d)
	}
}

// Пустой ответ инструмента не пустая строка.
func TestEmptyToolResultIsNotEmptyString(t *testing.T) {
	quiet := Tool{
		Spec: ollama.ToolSpec{Name: "тихий", Parameters: ollama.ToolParams{Type: "object"}},
		Run:  func(context.Context, map[string]any) (string, error) { return "   ", nil },
	}
	d := call(t, server(quiet),
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"тихий","arguments":{}}}`)
	text := d["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if strings.TrimSpace(text) == "" {
		t.Error("клиенту ушёл пустой текст: он не отличит его от потери ответа")
	}
}
