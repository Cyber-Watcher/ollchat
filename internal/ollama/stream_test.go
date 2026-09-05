package ollama

import (
	"context"
	"errors"
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
	return New(srv.URL, 10*time.Second, 0, nil)
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

	c := New(srv.URL, 10*time.Second, 0, nil)
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

// TestChatHasSeparateHeaderTimeout проверяет разделение таймаутов из
// TimeOutPlan.md: Ollama не шлёт ни байта ответа /api/chat, пока не
// обработает весь промпт, поэтому Chat() должен переживать паузу, которая
// обрывает короткий таймаут быстрых вызовов (Version/Tags/PS/Show). Именно
// смешение этих двух таймаутов на одном транспорте обрывало реальные диалоги
// на стенде при долгой обработке большого контекста.
func TestChatHasSeparateHeaderTimeout(t *testing.T) {
	const headerDelay = 150 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ни байта, пока не «обработали промпт» — как и настоящая Ollama.
		time.Sleep(headerDelay)
		switch r.URL.Path {
		case "/api/version":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"0.32.13"}`))
		case "/api/chat":
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write([]byte(streamFixture))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// timeout короче задержки сервера — быстрые вызовы должны обрываться.
	// chatTimeout заметно длиннее — Chat() должен дождаться ответа.
	c := New(srv.URL, headerDelay/3, headerDelay*6, nil)

	if _, err := c.Version(context.Background()); err == nil {
		t.Fatal("Version() с коротким timeout должен обрываться на задержке сервера")
	}

	var gotErr error
	var done bool
	for ev := range c.Chat(context.Background(), ChatRequest{Model: "qwen3.6:latest"}) {
		if ev.Kind == EventError {
			gotErr = ev.Err
		}
		if ev.Kind == EventDone {
			done = true
		}
	}
	if gotErr != nil {
		t.Fatalf("Chat() с щедрым chatTimeout не должен обрываться на той же задержке: %v", gotErr)
	}
	if !done {
		t.Fatal("Chat() должен получить EventDone")
	}
}

// TestChatCancelStopsPromptly проверяет, что щедрый chatTimeout не ослабляет
// отмену пользователем (Esc/Ctrl+C): отмена контекста должна прерывать Chat()
// сразу, не дожидаясь ни срабатывания таймаута, ни ответа сервера.
func TestChatCancelStopsPromptly(t *testing.T) {
	// Go не обязан рвать TCP-соединение сразу при отмене контекста клиентом —
	// проверено: srv.Close() дожидается конца обработчика, а не отмены
	// (см. TimeOutPlan.md). Поэтому держим паузу сервера маленькой —
	// проверяем только то, что реально важно: скорость возврата у клиента.
	const serverDelay = 300 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(serverDelay)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(streamFixture))
	}))
	defer srv.Close()

	c := New(srv.URL, time.Second, 10*time.Second, nil)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	started := time.Now()
	var gotErr error
	for ev := range c.Chat(ctx, ChatRequest{Model: "qwen3.6:latest"}) {
		if ev.Kind == EventError {
			gotErr = ev.Err
		}
	}
	elapsed := time.Since(started)

	if !errors.Is(gotErr, ErrCanceled) {
		t.Fatalf("ожидался ErrCanceled, получено: %v", gotErr)
	}
	if elapsed > serverDelay {
		t.Errorf("отмена сработала за %v — щедрый chatTimeout не должен её задерживать", elapsed)
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
