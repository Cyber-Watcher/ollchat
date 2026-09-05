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
	if _, ok := r["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("сервер не объявил, что умеет инструменты")
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

// Битое сообщение.
func TestBrokenMessage(t *testing.T) {
	d := call(t, server(), `{это не json`)
	e := d["error"].(map[string]any)
	if int(e["code"].(float64)) != codeParse {
		t.Errorf("код ошибки разбора = %v", e["code"])
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
