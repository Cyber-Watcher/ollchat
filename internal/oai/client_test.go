package oai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Короткий завершённый поток: пустой ответ, конец, [DONE].
const okStream = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ок\"},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

// captured — что сервер-перехватчик увидел в запросе.
type captured struct {
	mu     sync.Mutex
	path   string
	method string
	header http.Header
	body   map[string]any
}

// interceptor отдаёт reply на любой запрос и запоминает последний запрос.
func interceptor(t *testing.T, status int, reply string) (*httptest.Server, *captured) {
	t.Helper()
	cp := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		cp.mu.Lock()
		cp.path, cp.method, cp.header, cp.body = r.URL.Path, r.Method, r.Header.Clone(), body
		cp.mu.Unlock()
		if status == http.StatusOK {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, cp
}

func chatOnce(c *Client, req ollama.ChatRequest) collected {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return collect(c.Chat(ctx, req))
}

const (
	pngB64  = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	jpegB64 = "/9j/4AAQSkZJRgABAQ"
)

func TestRequestBody(t *testing.T) {
	srv, cp := interceptor(t, http.StatusOK, okStream)
	c := New(srv.URL, "", 0, 0, 0, map[string]string{"X-Probe": "да"})
	think := false
	paris := map[string]any{"city": "Париж"}
	req := ollama.ChatRequest{
		Model: "qwen38",
		Messages: []ollama.Message{
			{Role: ollama.RoleSystem, Content: "система"},
			{Role: ollama.RoleUser, Content: "Что на картинках?", Images: []string{pngB64, jpegB64}},
			// Ответ Ollama: вызовы без id, рядом мышление.
			{Role: ollama.RoleAssistant, Thinking: "думаю", ToolCalls: []ollama.ToolCall{
				{Function: ollama.ToolCallFunc{Name: "get_weather", Arguments: paris}},
				{Function: ollama.ToolCallFunc{Name: "get_time", Arguments: paris}},
			}},
			// Ответы инструментов без tool_call_id и не по порядку вызовов.
			{Role: ollama.RoleTool, ToolName: "get_time", Content: "14:05"},
			{Role: ollama.RoleTool, ToolName: "get_weather", Content: "+12 °C"},
			// Ответ OpenAI-сервера: id есть и должен уйти как есть.
			{Role: ollama.RoleAssistant, ToolCalls: []ollama.ToolCall{
				{ID: "srv-1", Function: ollama.ToolCallFunc{Name: "get_time", Arguments: paris}},
			}},
			{Role: ollama.RoleTool, ToolName: "get_time", ToolCallID: "srv-1", Content: "14:06"},
			// Вызов без доводов и ответ без имени: привязка по порядку.
			{Role: ollama.RoleAssistant, ToolCalls: []ollama.ToolCall{
				{Function: ollama.ToolCallFunc{Name: "list"}},
			}},
			{Role: ollama.RoleTool, Content: "пусто"},
		},
		Tools: []ollama.Tool{{Type: "function", Function: ollama.ToolSpec{Name: "get_weather",
			Description: "погода", Parameters: ollama.ToolParams{Type: "object",
				Properties: map[string]ollama.ToolProp{"city": {Type: "string"}}, Required: []string{"city"}}}}},
		Think:     &think,
		KeepAlive: "5m",
		Options: map[string]any{"num_predict": int64(77), "num_ctx": 8192, "temperature": 0.0,
			"seed": 42, "top_p": 0.9, "stop": []string{"###"}, "num_batch": 512},
		Logprobs: true,
	}
	if r := chatOnce(c, req); r.err != nil || !r.done {
		t.Fatalf("обмен не прошёл: %v", r.err)
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	b := cp.body
	if cp.method != http.MethodPost || cp.path != "/v1/chat/completions" {
		t.Errorf("запрос %s %s", cp.method, cp.path)
	}
	if b["model"] != "qwen38" || b["stream"] != true {
		t.Errorf("model/stream: %v %v", b["model"], b["stream"])
	}
	if so, _ := b["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Errorf("stream_options: %v", b["stream_options"])
	}
	if b["max_tokens"] != 77.0 || b["temperature"] != 0.0 || b["seed"] != 42.0 || b["top_p"] != 0.9 {
		t.Errorf("параметры: max_tokens %v temperature %v seed %v top_p %v",
			b["max_tokens"], b["temperature"], b["seed"], b["top_p"])
	}
	if stop, _ := b["stop"].([]any); len(stop) != 1 || stop[0] != "###" {
		t.Errorf("stop: %v", b["stop"])
	}
	for _, k := range []string{"options", "num_ctx", "num_predict", "num_batch", "keep_alive", "think"} {
		if _, ok := b[k]; ok {
			t.Errorf("в теле лишнее поле %q", k)
		}
	}
	if kw, _ := b["chat_template_kwargs"].(map[string]any); kw["enable_thinking"] != false {
		t.Errorf("chat_template_kwargs: %v", b["chat_template_kwargs"])
	}
	if b["logprobs"] != true {
		t.Errorf("logprobs: %v", b["logprobs"])
	}
	if tools, _ := b["tools"].([]any); len(tools) != 1 {
		t.Errorf("tools: %v", b["tools"])
	}

	msgs, _ := b["messages"].([]any)
	if len(msgs) != len(req.Messages) {
		t.Fatalf("сообщений %d, ждали %d", len(msgs), len(req.Messages))
	}
	msg := func(i int) map[string]any { m, _ := msgs[i].(map[string]any); return m }

	// Картинки — частями content.
	parts, _ := msg(1)["content"].([]any)
	if len(parts) != 3 {
		t.Fatalf("частей content %d: %v", len(parts), msg(1)["content"])
	}
	if p, _ := parts[0].(map[string]any); p["type"] != "text" || p["text"] != "Что на картинках?" {
		t.Errorf("текстовая часть: %v", p)
	}
	for i, prefix := range []string{"data:image/png;base64," + pngB64, "data:image/jpeg;base64," + jpegB64} {
		p, _ := parts[i+1].(map[string]any)
		iu, _ := p["image_url"].(map[string]any)
		if p["type"] != "image_url" || iu["url"] != prefix {
			t.Errorf("картинка %d: %v", i, p)
		}
	}
	if _, ok := msg(1)["images"]; ok {
		t.Error("поле images ушло в запрос")
	}

	// Вызовы: аргументы строкой, id дописан, мышление не ушло.
	asst := msg(2)
	if _, ok := asst["thinking"]; ok {
		t.Error("мышление ушло в историю")
	}
	calls, _ := asst["tool_calls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("вызовов в истории %d", len(calls))
	}
	for i, wantID := range []string{"call_2_0", "call_2_1"} {
		tc, _ := calls[i].(map[string]any)
		fn, _ := tc["function"].(map[string]any)
		args, isStr := fn["arguments"].(string)
		if !isStr || args != `{"city":"Париж"}` {
			t.Errorf("вызов %d: arguments %#v, ждали строку JSON", i, fn["arguments"])
		}
		if tc["id"] != wantID || tc["type"] != "function" {
			t.Errorf("вызов %d: id %v type %v", i, tc["id"], tc["type"])
		}
	}
	// Ответы инструментов привязаны по имени, а не по порядку.
	if msg(3)["tool_call_id"] != "call_2_1" || msg(4)["tool_call_id"] != "call_2_0" {
		t.Errorf("tool_call_id: %v %v", msg(3)["tool_call_id"], msg(4)["tool_call_id"])
	}
	if _, ok := msg(3)["tool_name"]; ok {
		t.Error("tool_name ушло в запрос")
	}
	if c5, _ := msg(5)["tool_calls"].([]any); len(c5) != 1 || c5[0].(map[string]any)["id"] != "srv-1" {
		t.Errorf("id сервера не сохранён: %v", msg(5)["tool_calls"])
	}
	if msg(6)["tool_call_id"] != "srv-1" {
		t.Errorf("tool_call_id заданный: %v", msg(6)["tool_call_id"])
	}
	c7, _ := msg(7)["tool_calls"].([]any)
	if fn, _ := c7[0].(map[string]any)["function"].(map[string]any); fn["arguments"] != "{}" {
		t.Errorf("пустые доводы: %#v", fn["arguments"])
	}
	if msg(8)["tool_call_id"] != "call_7_0" {
		t.Errorf("ответ без имени не привязан по порядку: %v", msg(8)["tool_call_id"])
	}

	if got := cp.header.Get("X-Probe"); got != "да" {
		t.Errorf("заголовок сервера не дошёл: %q", got)
	}
	if got := cp.header.Get("Authorization"); got != "" {
		t.Errorf("без ключа ушёл Authorization: %q", got)
	}
}

func TestRequestDefaults(t *testing.T) {
	srv, cp := interceptor(t, http.StatusOK, okStream)
	c := New(srv.URL, "", 0, 0, 0, nil)
	if r := chatOnce(c, ollama.ChatRequest{Model: "m", Options: map[string]any{"num_predict": -1}}); r.err != nil {
		t.Fatal(r.err)
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	for _, k := range []string{"max_tokens", "chat_template_kwargs", "logprobs", "temperature", "tools"} {
		if _, ok := cp.body[k]; ok {
			t.Errorf("поле %q без запроса на него: %v", k, cp.body[k])
		}
	}
}

func TestAPIKey(t *testing.T) {
	const key = "sk-test-0123456789abcdef"
	// Шлюз, повторяющий ключ в ответе 401: ключ не должен попасть в ошибку.
	srv, cp := interceptor(t, http.StatusUnauthorized,
		`{"error":{"message":"Authentication Error, invalid key = `+key+`","type":"auth_error","code":"401"}}`)
	c := New(srv.URL, key+"\n", 0, 0, 0, map[string]string{"Authorization": "Bearer старый"})

	r := chatOnce(c, ollama.ChatRequest{Model: "m"})
	if r.err == nil {
		t.Fatal("401 прошёл без ошибки")
	}
	if strings.Contains(r.err.Error(), key) {
		t.Errorf("ключ в тексте ошибки чата: %v", r.err)
	}
	if ollama.Retryable(r.err) {
		t.Errorf("401 помечен повторяемым: %v", r.err)
	}
	if !strings.Contains(r.err.Error(), "401") || !strings.Contains(r.err.Error(), "Authentication Error") {
		t.Errorf("в ошибке нет кода и слов сервера: %v", r.err)
	}
	cp.mu.Lock()
	got := cp.header.Get("Authorization")
	cp.mu.Unlock()
	if got != "Bearer "+key {
		t.Errorf("Authorization %q", got)
	}

	_, err := c.Models(context.Background())
	if err == nil || strings.Contains(err.Error(), key) {
		t.Errorf("Models: ошибка %v (пусто или с ключом)", err)
	}
	cp.mu.Lock()
	got = cp.header.Get("Authorization")
	cp.mu.Unlock()
	if got != "Bearer "+key {
		t.Errorf("Models: Authorization %q", got)
	}
}

func TestBaseURL(t *testing.T) {
	srv, cp := interceptor(t, http.StatusOK, okStream)
	for _, base := range []string{srv.URL, srv.URL + "/", srv.URL + "/v1", srv.URL + "/v1/"} {
		c := New(base, "", 0, 0, 0, nil)
		if want := strings.TrimRight(base, "/"); c.BaseURL() != want {
			t.Errorf("%s: BaseURL %q", base, c.BaseURL())
		}
		if r := chatOnce(c, ollama.ChatRequest{Model: "m"}); r.err != nil {
			t.Errorf("%s: %v", base, r.err)
		}
		cp.mu.Lock()
		path := cp.path
		cp.mu.Unlock()
		if path != "/v1/chat/completions" {
			t.Errorf("%s: путь %q", base, path)
		}
		_, _ = c.Models(context.Background()) // ответ — поток, разбор не важен
		cp.mu.Lock()
		path = cp.path
		cp.mu.Unlock()
		if path != "/v1/models" {
			t.Errorf("%s: путь Models %q", base, path)
		}
	}
}

func TestStatusRetryable(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      string
		retryable bool
		text      string
	}{
		{http.StatusServiceUnavailable, `{"error":{"message":"движок перезапускается"}}`, true, "движок перезапускается"},
		{http.StatusTooManyRequests, `{"error":{"message":"rate limit"}}`, true, "rate limit"},
		{http.StatusInternalServerError, `не JSON вовсе`, true, "не JSON вовсе"},
		{http.StatusBadRequest, `{"object":"error","message":"bad request text"}`, false, "bad request text"},
		{http.StatusNotFound, `{"error":"model not found"}`, false, "model not found"},
	} {
		srv, _ := interceptor(t, tc.status, tc.body)
		r := chatOnce(New(srv.URL, "", 0, 0, 0, nil), ollama.ChatRequest{Model: "m"})
		if r.err == nil {
			t.Errorf("%d: без ошибки", tc.status)
			continue
		}
		if ollama.Retryable(r.err) != tc.retryable {
			t.Errorf("%d: повторяемая %v, ждали %v", tc.status, ollama.Retryable(r.err), tc.retryable)
		}
		if !strings.HasPrefix(r.err.Error(), "сервер вернул ") || !strings.HasSuffix(r.err.Error(), ": "+tc.text) {
			t.Errorf("%d: текст %q", tc.status, r.err.Error())
		}
	}
}

func TestNetworkErrorRetryable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	r := chatOnce(New(url, "", 0, 0, 0, nil), ollama.ChatRequest{Model: "m"})
	if r.err == nil || !ollama.Retryable(r.err) {
		t.Errorf("отказ в соединении: %v, повторяемая %v", r.err, ollama.Retryable(r.err))
	}
}

func TestStreamCutWithoutDone(t *testing.T) {
	srv, _ := interceptor(t, http.StatusOK,
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"нача\"}}]}\n\n")
	r := chatOnce(New(srv.URL, "", 0, 0, 0, nil), ollama.ChatRequest{Model: "m"})
	if r.content != "нача" {
		t.Errorf("кусок до обрыва потерян: %q", r.content)
	}
	if r.done || r.err == nil || !ollama.Retryable(r.err) || !strings.Contains(r.err.Error(), "оборван") {
		t.Errorf("обрыв без [DONE]: завершён %v, ошибка %v", r.done, r.err)
	}
}

func TestNonStreamAnswer(t *testing.T) {
	// Ответ целиком, как в записях tools-nostream: не поток — не обрыв.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"content":"всё"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)
	r := chatOnce(New(srv.URL, "", 0, 0, 0, nil), ollama.ChatRequest{Model: "m"})
	if r.err == nil || ollama.Retryable(r.err) || !strings.Contains(r.err.Error(), "не потоком") {
		t.Errorf("ответ не потоком: %v", r.err)
	}
}

func TestFinishWithoutDone(t *testing.T) {
	// Конец ответа назван, [DONE] не пришёл — ответ полный.
	srv, _ := interceptor(t, http.StatusOK,
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"всё\"},\"finish_reason\":\"length\"}]}\n\n")
	r := chatOnce(New(srv.URL, "", 0, 0, 0, nil), ollama.ChatRequest{Model: "m"})
	if r.err != nil || !r.done || r.stats.DoneReason != "length" || r.content != "всё" {
		t.Errorf("ошибка %v, завершён %v, DoneReason %q, текст %q", r.err, r.done, r.stats.DoneReason, r.content)
	}
}

func TestErrorInsideStream(t *testing.T) {
	for _, line := range []string{
		`{"error":{"message":"сбой разбора вывода","type":"InternalServerError","code":500}}`,
		`{"object":"error","message":"сбой разбора вывода"}`,
	} {
		srv, _ := interceptor(t, http.StatusOK,
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"а\"}}]}\n\ndata: "+line+"\n\n")
		r := chatOnce(New(srv.URL, "", 0, 0, 0, nil), ollama.ChatRequest{Model: "m"})
		if r.err == nil || !ollama.Retryable(r.err) || r.err.Error() != "сбой разбора вывода" {
			t.Errorf("%s: ошибка %v", line, r.err)
		}
	}
}

func TestToolArguments(t *testing.T) {
	// Вызов 0 оборван посреди строки, у вызова 1 доводов нет вовсе; куски
	// вызовов перемежаются, порядок — по index.
	stream := strings.Join([]string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"list","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"get_weather","arguments":"{\"city\": \"Пар"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		`[DONE]`,
	}, "\n\ndata: ")
	srv, _ := interceptor(t, http.StatusOK, "data: "+stream+"\n\n")
	r := chatOnce(New(srv.URL, "", 0, 0, 0, nil), ollama.ChatRequest{Model: "m"})
	if r.err != nil || r.callEvents != 1 || len(r.calls) != 2 {
		t.Fatalf("ошибка %v, событий %d, вызовов %d", r.err, r.callEvents, len(r.calls))
	}
	if r.calls[0].ID != "a" || r.calls[0].Function.Arguments["_raw"] != `{"city": "Пар` {
		t.Errorf("оборванные доводы: %+v", r.calls[0])
	}
	if r.calls[1].ID != "b" || r.calls[1].Function.Arguments == nil || len(r.calls[1].Function.Arguments) != 0 {
		t.Errorf("пустые доводы: %+v", r.calls[1])
	}
	if r.stats.DoneReason != "length" {
		t.Errorf("DoneReason %q", r.stats.DoneReason)
	}
}

func TestLogprobsAnswerOnly(t *testing.T) {
	stream := strings.Join([]string{
		`{"choices":[{"index":0,"delta":{"reasoning":"хм"},"logprobs":{"content":[{"token":"хм","logprob":-5}]}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"да"},"logprobs":{"content":[{"token":"д","logprob":-0.1},{"token":"а","logprob":-1}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	}, "\n\ndata: ")
	srv, _ := interceptor(t, http.StatusOK, "data: "+stream+"\n\n")
	r := chatOnce(New(srv.URL, "", 0, 0, 0, nil), ollama.ChatRequest{Model: "m", Logprobs: true})
	s := r.stats
	if r.err != nil || s.LogprobN != 2 || s.LogprobMin != -1 || s.LogprobLow != 1 || s.LogprobSum > -1.09 || s.LogprobSum < -1.11 {
		t.Errorf("ошибка %v, сводка N %d сумма %v мин %v низких %d", r.err, s.LogprobN, s.LogprobSum, s.LogprobMin, s.LogprobLow)
	}
	if r.thinking != "хм" || r.content != "да" {
		t.Errorf("мышление %q, текст %q", r.thinking, r.content)
	}
}

// hangingServer шлёт один кусок ответа и молчит, пока тест не кончится.
func hangingServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"а\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // раньше srv.Close: Cleanup идут с конца
	return srv
}

func TestCancel(t *testing.T) {
	srv := hangingServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := New(srv.URL, "", 0, 0, 0, nil).Chat(ctx, ollama.ChatRequest{Model: "m"})
	var last ollama.Event
	for ev := range ch {
		if ev.Kind == ollama.EventContent {
			cancel()
		}
		last = ev
	}
	if last.Kind != ollama.EventError || !errors.Is(last.Err, ollama.ErrCanceled) {
		t.Errorf("последнее событие %v: %v", last.Kind, last.Err)
	}
}

func TestStall(t *testing.T) {
	srv := hangingServer(t)
	c := New(srv.URL, "", 0, 0, time.Second, nil)
	start := time.Now()
	r := chatOnce(c, ollama.ChatRequest{Model: "m"})
	if r.err == nil || !ollama.Retryable(r.err) || !strings.Contains(r.err.Error(), "stall_timeout") {
		t.Errorf("молчание: %v", r.err)
	}
	if errors.Is(r.err, ollama.ErrCanceled) {
		t.Error("молчание выдано за отмену человеком")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("сторож сработал через %s", d)
	}
}

func TestModelsBodyLimit(t *testing.T) {
	srv, _ := interceptor(t, http.StatusOK, `{"data":[{"id":"`+strings.Repeat("x", 200)+`"}]}`)
	c := New(srv.URL, "", 0, 0, 0, nil)
	c.maxBody = 100
	if _, err := c.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "предела") {
		t.Errorf("ответ длиннее предела: %v", err)
	}
}

func TestModelsLenientNumbers(t *testing.T) {
	srv, _ := interceptor(t, http.StatusOK,
		`{"data":[{"id":"a","max_model_len":"4096"},{"id":"b","context_length":null},{"id":"c","meta":{"n_ctx":8192.0}}]}`)
	models, err := New(srv.URL, "", 0, 0, 0, nil).Models(context.Background())
	if err != nil || len(models) != 3 {
		t.Fatalf("%v %v", models, err)
	}
	for i, want := range []int{4096, 0, 8192} {
		if models[i].ContextLength != want {
			t.Errorf("%s: окно %d, ждали %d", models[i].ID, models[i].ContextLength, want)
		}
	}
}

func TestImageMIME(t *testing.T) {
	for b64, want := range map[string]string{
		pngB64: "image/png", jpegB64: "image/jpeg", "R0lGODlhAQABAA": "image/gif",
		"UklGRiQAAABXRUJQ": "image/webp", "Qk0eAAAAAAAAABoAAAA": "image/png",
	} {
		if got := imageMIME(b64); got != want {
			t.Errorf("%.8s…: %s, ждали %s", b64, got, want)
		}
	}
}

func TestSSEParser(t *testing.T) {
	in := byteOrderMark + "data: a\r\ndata: b\r\n\r\n: комментарий\n\nevent: x\nid: 1\n\ndata:c\r\rdata: оборвано"
	s := newSSE(strings.NewReader(in))
	var got []string
	for {
		d, err := s.next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			break
		}
		got = append(got, d)
	}
	if strings.Join(got, "|") != "a\nb|c" {
		t.Errorf("события %q", got)
	}
}
