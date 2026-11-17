package oai

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Поле priority уходит только у запроса с большим новым входом и только при
// заданном WithPriority: движок без политики приоритета запрос с полем отвергает.
// Новый вход — хвост после последнего ответа модели: длинная история, которую
// движок берёт из кеша префиксов, запрос «длинным» не делает.
func TestPriorityOnlyForLargeNewInput(t *testing.T) {
	const reply = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	big := strings.Repeat("слово ", 4000) // 6 байт × 4000 = 24000 байт ≈ 8000 токенов по ctxmeter.Estimate
	cases := []struct {
		name string
		msgs []ollama.Message
		want any
	}{
		{"короткий вопрос", []ollama.Message{{Role: ollama.RoleUser, Content: "привет"}}, nil},
		{"вставленный документ", []ollama.Message{{Role: ollama.RoleUser, Content: big}}, float64(10)},
		{"большой вывод инструмента", []ollama.Message{
			{Role: ollama.RoleUser, Content: "прочитай файл"},
			{Role: ollama.RoleAssistant, ToolCalls: []ollama.ToolCall{{Function: ollama.ToolCallFunc{Name: "read_file"}}}},
			{Role: ollama.RoleTool, Content: big}}, float64(10)},
		{"длинная история, короткий новый вопрос", []ollama.Message{
			{Role: ollama.RoleUser, Content: big},
			{Role: ollama.RoleAssistant, Content: "ответ"},
			{Role: ollama.RoleUser, Content: "а ещё?"}}, nil},
		{"системный промпт не считается новым входом", []ollama.Message{
			{Role: ollama.RoleSystem, Content: big},
			{Role: ollama.RoleUser, Content: "привет"}}, nil},
	}
	for _, c := range cases {
		srv, cp := interceptor(t, http.StatusOK, reply)
		chatOnce(New(srv.URL, "", 0, 0, 0, nil).WithPriority(5000, 10), ollama.ChatRequest{Model: "m", Messages: c.msgs})
		cp.mu.Lock()
		got, has := cp.body["priority"]
		cp.mu.Unlock()
		if c.want == nil && has {
			t.Errorf("%s: priority = %v, ждали без поля", c.name, got)
		}
		if c.want != nil && got != c.want {
			t.Errorf("%s: priority = %v, ждали %v", c.name, got, c.want)
		}
	}

	// Без WithPriority поля нет даже у огромного входа.
	srv, cp := interceptor(t, http.StatusOK, reply)
	chatOnce(New(srv.URL, "", 0, 0, 0, nil),
		ollama.ChatRequest{Model: "m", Messages: []ollama.Message{{Role: ollama.RoleUser, Content: big}}})
	cp.mu.Lock()
	_, has := cp.body["priority"]
	cp.mu.Unlock()
	if has {
		t.Error("без WithPriority поле priority уходит в запрос")
	}
}
