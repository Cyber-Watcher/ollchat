package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Фикстуры взяты из реальных ответов Ollama 0.32.7 на стенде.
const streamFixture = `{"model":"qwen3.6:latest","created_at":"2026-08-11T08:46:49.844Z","message":{"role":"assistant","content":"","thinking":"Раз"},"done":false}
{"model":"qwen3.6:latest","created_at":"2026-08-11T08:46:49.858Z","message":{"role":"assistant","content":"","thinking":"мышляю"},"done":false}
{"model":"qwen3.6:latest","created_at":"2026-08-11T08:46:50.000Z","message":{"role":"assistant","content":"Ответ"},"done":false}
{"model":"qwen3.6:latest","created_at":"2026-08-11T08:46:50.100Z","message":{"role":"assistant","content":": 4"},"done":false}
{"model":"qwen3.6:latest","created_at":"2026-08-11T08:46:50.200Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","total_duration":14133740044,"load_duration":632849158,"prompt_eval_count":16,"prompt_eval_duration":2308911000,"eval_count":9,"eval_duration":83463000}
`

const toolCallFixture = `{"model":"qwen3.6:latest","created_at":"2026-08-11T09:00:50.789Z","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_lj18tar0","function":{"index":0,"name":"read_file","arguments":{"path":"/etc/hostname"}}}]},"done":false}
{"model":"qwen3.6:latest","created_at":"2026-08-11T09:00:50.799Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","total_duration":63018935871,"prompt_eval_count":299,"eval_count":27,"eval_duration":245243000}
`

func serveFixture(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, 10*time.Second, nil)
}

func TestChatStreamParsing(t *testing.T) {
	c := serveFixture(t, streamFixture)

	var content, thinking string
	var stats Stats
	var done bool

	for ev := range c.Chat(context.Background(), ChatRequest{Model: "qwen3.6:latest"}) {
		switch ev.Kind {
		case EventContent:
			content += ev.Text
		case EventThinking:
			thinking += ev.Text
		case EventDone:
			stats = ev.Stats
			done = true
		case EventError:
			t.Fatalf("неожиданная ошибка: %v", ev.Err)
		}
	}

	if !done {
		t.Fatal("событие завершения не получено")
	}
	if content != "Ответ: 4" {
		t.Errorf("текст ответа = %q, ожидалось %q", content, "Ответ: 4")
	}
	if thinking != "Размышляю" {
		t.Errorf("рассуждения = %q, ожидалось %q", thinking, "Размышляю")
	}
	if stats.PromptEvalCount != 16 || stats.EvalCount != 9 {
		t.Errorf("счётчики токенов = %d/%d, ожидалось 16/9", stats.PromptEvalCount, stats.EvalCount)
	}
	if got := stats.TotalTokens(); got != 25 {
		t.Errorf("занято токенов = %d, ожидалось 25", got)
	}
	if tps := stats.TokensPerSecond(); tps < 100 || tps > 120 {
		t.Errorf("скорость = %.1f ток/с, ожидалось около 108", tps)
	}
}

func TestChatToolCallParsing(t *testing.T) {
	c := serveFixture(t, toolCallFixture)

	var calls []ToolCall
	for ev := range c.Chat(context.Background(), ChatRequest{Model: "qwen3.6:latest"}) {
		if ev.Kind == EventToolCalls {
			calls = append(calls, ev.ToolCalls...)
		}
		if ev.Kind == EventError {
			t.Fatalf("неожиданная ошибка: %v", ev.Err)
		}
	}

	if len(calls) != 1 {
		t.Fatalf("получено вызовов инструментов: %d, ожидался 1", len(calls))
	}
	call := calls[0]
	if call.ID != "call_lj18tar0" {
		t.Errorf("идентификатор вызова = %q", call.ID)
	}
	if call.Function.Name != "read_file" {
		t.Errorf("имя инструмента = %q", call.Function.Name)
	}
	// Аргументы приходят объектом, а не строкой — это отличие Ollama от OpenAI.
	path, ok := call.Function.Arguments["path"].(string)
	if !ok || path != "/etc/hostname" {
		t.Errorf("аргумент path = %v, ожидалось /etc/hostname", call.Function.Arguments["path"])
	}
	if got := call.Function.ArgumentsJSON(); got != `{"path":"/etc/hostname"}` {
		t.Errorf("ArgumentsJSON = %s", got)
	}
}

func TestChatTruncatedStream(t *testing.T) {
	// Поток без финального чанка должен превращаться в ошибку, а не в тихий успех.
	c := serveFixture(t, `{"model":"m","message":{"role":"assistant","content":"нача"},"done":false}`+"\n")

	var gotErr bool
	for ev := range c.Chat(context.Background(), ChatRequest{Model: "m"}) {
		if ev.Kind == EventError {
			gotErr = true
		}
		if ev.Kind == EventDone {
			t.Fatal("завершение не должно приходить для оборванного потока")
		}
	}
	if !gotErr {
		t.Error("оборванный поток должен давать ошибку")
	}
}

func TestChatServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model 'nope' not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, 10*time.Second, nil)
	var gotErr error
	for ev := range c.Chat(context.Background(), ChatRequest{Model: "nope"}) {
		if ev.Kind == EventError {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("ошибка сервера должна доходить до вызывающего кода")
	}
}

func TestContextLengthFromShow(t *testing.T) {
	// Ключ зависит от семейства модели — ищем по суффиксу.
	show := &ShowResponse{ModelInfo: map[string]any{
		"gemma3.attention.head_count": float64(8),
		"gemma3.context_length":       float64(131072),
		"general.architecture":        "gemma3",
	}}
	n, ok := ContextLengthFromShow(show)
	if !ok || n != 131072 {
		t.Errorf("ContextLengthFromShow = %d, %v; ожидалось 131072, true", n, ok)
	}

	if _, ok := ContextLengthFromShow(&ShowResponse{ModelInfo: map[string]any{"x": float64(1)}}); ok {
		t.Error("при отсутствии ключа должно возвращаться false")
	}
	if _, ok := ContextLengthFromShow(nil); ok {
		t.Error("для nil должно возвращаться false")
	}
}
