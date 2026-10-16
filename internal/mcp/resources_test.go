package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// fakeTool — инструмент службы с заданным именем и ответом.
func fakeTool(name string, run func(args map[string]any) string) Tool {
	return Tool{
		Spec: ollama.ToolSpec{Name: name, Description: name,
			Parameters: ollama.ToolParams{Type: "object", Properties: map[string]ollama.ToolProp{}}},
		Run: func(_ context.Context, args map[string]any) (string, error) { return run(args), nil },
	}
}

func resourceServer() *Server {
	return NewServer(nil,
		fakeTool("kb_read", func(a map[string]any) string { s, _ := a["id"].(string); return "кусок " + s }),
		fakeTool("kb_status", func(map[string]any) string { return "книг 3" }))
}

func result(t *testing.T, d map[string]any) map[string]any {
	t.Helper()
	r, ok := d["result"].(map[string]any)
	if !ok {
		t.Fatalf("ответ без результата: %v", d)
	}
	return r
}

// Этап 112, С12: служба объявляет ресурсы и промпты и отдаёт кусок библиотеки
// по адресу kb://<коллекция>/<книга>/<кусок> тем же путём, что kb_read.
func TestResourcesReadChunkAndStatus(t *testing.T) {
	s := resourceServer()
	caps := result(t, call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))["capabilities"].(map[string]any)
	if caps["resources"] == nil || caps["prompts"] == nil {
		t.Fatalf("возможности без ресурсов и промптов: %v", caps)
	}

	list := result(t, call(t, s, `{"jsonrpc":"2.0","id":2,"method":"resources/list"}`))["resources"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["uri"] != "kb://status" {
		t.Errorf("список ресурсов %v, ждали kb://status", list)
	}
	tpl := result(t, call(t, s, `{"jsonrpc":"2.0","id":3,"method":"resources/templates/list"}`))["resourceTemplates"].([]any)
	if len(tpl) != 1 {
		t.Errorf("шаблонов адреса %d, ждали 1", len(tpl))
	}

	for uri, want := range map[string]string{"kb://books/167/1": "кусок books/167#1", "kb://status": "книг 3"} {
		r := result(t, call(t, s, `{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"`+uri+`"}}`))
		c := r["contents"].([]any)[0].(map[string]any)
		if c["text"] != want || c["uri"] != uri {
			t.Errorf("%s: прочитано %v, ждали %q", uri, c, want)
		}
	}

	for _, bad := range []string{"kb://books", "http://books/1/2", "kb://books/167/"} {
		d := call(t, s, `{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"`+bad+`"}}`)
		if e, _ := d["error"].(map[string]any); e == nil || int(e["code"].(float64)) != codeResourceNotFound {
			t.Errorf("%s: ждали ошибку «ресурс не найден», получено %v", bad, d)
		}
	}
}

// Выключенный kb_read (Ц6) не читается и ресурсом, и шаблона куска нет.
func TestResourcesRespectDisabledTools(t *testing.T) {
	s := resourceServer()
	s.Policy.Disabled = map[string]bool{"kb_read": true}
	if tpl := result(t, call(t, s, `{"jsonrpc":"2.0","id":1,"method":"resources/templates/list"}`))["resourceTemplates"].([]any); len(tpl) != 0 {
		t.Errorf("kb_read выключен, а шаблон куска есть: %v", tpl)
	}
	d := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"kb://books/167/1"}}`)
	if e, _ := d["error"].(map[string]any); e == nil || !strings.Contains(e["message"].(string), "выключен") {
		t.Errorf("кусок прочитан при выключенном kb_read: %v", d)
	}
}

// Промпты: два сценария поиска; без обязательного аргумента — ошибка.
func TestPrompts(t *testing.T) {
	s := resourceServer()
	ps := result(t, call(t, s, `{"jsonrpc":"2.0","id":1,"method":"prompts/list"}`))["prompts"].([]any)
	if len(ps) != 2 {
		t.Fatalf("промптов %d, ждали 2", len(ps))
	}
	r := result(t, call(t, s, `{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"search_docs","arguments":{"question":"как устроен поиск","collection":"docs"}}}`))
	text := r["messages"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"].(string)
	if !strings.Contains(text, "как устроен поиск") || !strings.Contains(text, `collection: "docs"`) {
		t.Errorf("текст промпта без вопроса или коллекции: %q", text)
	}
	d := call(t, s, `{"jsonrpc":"2.0","id":3,"method":"prompts/get","params":{"name":"search_library","arguments":{}}}`)
	if d["error"] == nil {
		t.Errorf("промпт без вопроса собран: %v", d)
	}
}

func TestChunkURIRoundTrip(t *testing.T) {
	for _, id := range []string{"books/167#1", "projectdocs/7131#413"} {
		if got, ok := chunkID(chunkURI(id)); !ok || got != id {
			t.Errorf("%s → %s → %s (%v)", id, chunkURI(id), got, ok)
		}
	}
}
