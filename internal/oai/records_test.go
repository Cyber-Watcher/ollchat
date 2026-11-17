package oai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Мок-тесты на живых записях этапа 127, Ш1: ответы vLLM, SGLang,
// llama-server и LiteLLM (два псевдонима поверх vLLM), снятые 17.11.2026
// байт в байт (privatescripts/tuf/probe_ctx.py rec). Сервер-заглушка отдаёт
// запись как есть; клиент обязан разобрать её так же, как разобрал бы живой
// поток. В vllm/models.raw путь каталога моделей стенда заменён на выдуманный.

// recordServers — наборы записей, каталоги testdata/<сервер>/.
var recordServers = []string{"vllm", "sglang", "llamacpp", "litellm-qwen38-raw", "litellm-chat"}

// recordStatus — код ответа записи. Коды сняты в .meta рядом с записями
// (docs/plan100/qwen38_tests/s127rec/<сервер>/<случай>.meta); сами .meta
// в testdata не копируются: в них заголовки и время стенда.
func recordStatus(server, name string) int {
	switch name {
	case "over":
		return http.StatusBadRequest // у всех: 400
	case "image":
		switch server {
		case "sglang":
			return http.StatusOK // SGLang картинку понял
		case "llamacpp":
			return http.StatusInternalServerError // нет mmproj
		}
		return http.StatusBadRequest // vLLM поднят с --language-model-only, LiteLLM передал его ответ
	}
	return http.StatusOK
}

// serveRecord поднимает сервер, отдающий запись server/name на свой путь.
func serveRecord(t *testing.T, server, name string) *Client {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", server, name+".raw"))
	if err != nil {
		t.Fatal(err)
	}
	status := recordStatus(server, name)
	want := "/v1/chat/completions"
	ctype := "text/event-stream"
	if name == "models" {
		want = "/v1/models"
	}
	if name == "models" || status != http.StatusOK {
		ctype = "application/json"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != want {
			http.Error(w, "не тот путь: "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "", 10*time.Second, 10*time.Second, 0, nil)
}

// collected — всё, что отдал канал Chat.
type collected struct {
	content, thinking string
	calls             []ollama.ToolCall
	callEvents        int
	stats             ollama.Stats
	done              bool
	err               error
	afterEnd          int // событий после EventDone или EventError
}

func collect(ch <-chan ollama.Event) collected {
	var r collected
	ended := false
	for ev := range ch {
		if ended {
			r.afterEnd++
		}
		switch ev.Kind {
		case ollama.EventContent:
			r.content += ev.Text
		case ollama.EventThinking:
			r.thinking += ev.Text
		case ollama.EventToolCalls:
			r.callEvents++
			r.calls = append(r.calls, ev.ToolCalls...)
		case ollama.EventDone:
			r.done, r.stats, ended = true, ev.Stats, true
		case ollama.EventError:
			r.err, ended = ev.Err, true
		}
	}
	return r
}

// chatRecord — один обмен с записью; запрос сервер-заглушка не читает.
func chatRecord(t *testing.T, server, name string) collected {
	t.Helper()
	c := serveRecord(t, server, name)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := collect(c.Chat(ctx, ollama.ChatRequest{Model: "qwen38",
		Messages: []ollama.Message{{Role: ollama.RoleUser, Content: "?"}}}))
	if r.afterEnd != 0 {
		t.Errorf("%s/%s: %d событий после завершающего", server, name, r.afterEnd)
	}
	return r
}

func TestRecordPlain(t *testing.T) {
	for _, s := range recordServers {
		r := chatRecord(t, s, "plain")
		if r.err != nil || !r.done {
			t.Errorf("%s: ошибка %v, завершён %v", s, r.err, r.done)
			continue
		}
		// Вопрос записи: «Столица Франции? Одно слово.»
		if r.content != "Париж" {
			t.Errorf("%s: текст %q, ждали «Париж»", s, r.content)
		}
		if r.stats.DoneReason != "stop" {
			t.Errorf("%s: DoneReason %q", s, r.stats.DoneReason)
		}
		if r.stats.PromptEvalCount <= 0 || r.stats.EvalCount <= 0 {
			t.Errorf("%s: usage не дошёл: промпт %d, ответ %d", s, r.stats.PromptEvalCount, r.stats.EvalCount)
		}
		if r.stats.TotalDuration <= 0 || r.stats.TotalDuration < r.stats.PromptEvalDuration {
			t.Errorf("%s: длительности %d/%d", s, r.stats.TotalDuration, r.stats.PromptEvalDuration)
		}
		if r.thinking != "" || r.callEvents != 0 {
			t.Errorf("%s: лишнее: мышление %q, вызовов %d", s, r.thinking, r.callEvents)
		}
	}
}

func TestRecordThink(t *testing.T) {
	for _, s := range recordServers {
		r := chatRecord(t, s, "think")
		if r.err != nil || !r.done {
			t.Errorf("%s: ошибка %v, завершён %v", s, r.err, r.done)
			continue
		}
		if strings.TrimSpace(r.thinking) == "" {
			t.Errorf("%s: мышление не разобрано (у vLLM поле reasoning, у прочих reasoning_content)", s)
		}
		// Вопрос записи: «Сколько будет 17 × 23?»
		if !strings.Contains(r.content, "391") {
			t.Errorf("%s: в ответе нет 391: %q", s, r.content)
		}
		if r.thinking != "" && strings.Contains(r.content, r.thinking) {
			t.Errorf("%s: мышление попало в текст ответа", s)
		}
	}
}

func TestRecordTools(t *testing.T) {
	for _, s := range recordServers {
		r := chatRecord(t, s, "tools")
		if r.err != nil || !r.done {
			t.Errorf("%s: ошибка %v, завершён %v", s, r.err, r.done)
			continue
		}
		if r.callEvents != 1 {
			t.Errorf("%s: событий с вызовами %d, ждали одно", s, r.callEvents)
		}
		if len(r.calls) != 2 {
			t.Errorf("%s: вызовов %d, ждали 2", s, len(r.calls))
			continue
		}
		for i, want := range []string{"get_weather", "get_time"} {
			tc := r.calls[i]
			if tc.Function.Name != want {
				t.Errorf("%s: вызов %d — %q, ждали %q", s, i, tc.Function.Name, want)
			}
			if tc.ID == "" {
				t.Errorf("%s: у вызова %d пустой id", s, i)
			}
			if city, _ := tc.Function.Arguments["city"].(string); city != "Париж" {
				t.Errorf("%s: вызов %d, аргументы %v", s, i, tc.Function.Arguments)
			}
		}
		if r.calls[0].ID == r.calls[1].ID {
			t.Errorf("%s: у двух вызовов один id %q", s, r.calls[0].ID)
		}
		if r.stats.DoneReason != "tool_calls" {
			t.Errorf("%s: DoneReason %q", s, r.stats.DoneReason)
		}
	}
}

func TestRecordToolResult(t *testing.T) {
	for _, s := range recordServers {
		r := chatRecord(t, s, "toolresult")
		if r.err != nil || !r.done {
			t.Errorf("%s: ошибка %v, завершён %v", s, r.err, r.done)
			continue
		}
		// Ответ инструмента в записи: «+12 °C, дождь».
		if !strings.Contains(r.content, "+12") {
			t.Errorf("%s: ответ не опирается на результат инструмента: %q", s, r.content)
		}
		if r.stats.DoneReason != "stop" || r.callEvents != 0 {
			t.Errorf("%s: DoneReason %q, вызовов %d", s, r.stats.DoneReason, r.callEvents)
		}
	}
}

func TestRecordOverWindow(t *testing.T) {
	// Слова сервера про окно — из тела записи каждого сервера.
	words := map[string]string{
		"vllm":               "maximum context length is 32768",
		"sglang":             "longer than the model's context length",
		"llamacpp":           "exceeds the available context size",
		"litellm-qwen38-raw": "ContextWindowExceededError",
		"litellm-chat":       "ContextWindowExceededError",
	}
	for _, s := range recordServers {
		r := chatRecord(t, s, "over")
		if r.err == nil {
			t.Errorf("%s: переполнение окна прошло без ошибки", s)
			continue
		}
		msg := r.err.Error()
		if ollama.Retryable(r.err) {
			t.Errorf("%s: переполнение окна помечено повторяемым: %v", s, msg)
		}
		if !strings.Contains(msg, words[s]) {
			t.Errorf("%s: в ошибке нет слов сервера %q: %s", s, words[s], msg)
		}
		rest, ok := strings.CutPrefix(msg, "сервер вернул 400 Bad Request: ")
		if !ok || strings.HasPrefix(rest, "{") {
			t.Errorf("%s: текст не вынут из JSON: %s", s, msg)
		}
	}
}

func TestRecordImage(t *testing.T) {
	for _, s := range recordServers {
		r := chatRecord(t, s, "image")
		status := recordStatus(s, "image")
		if status == http.StatusOK {
			if r.err != nil || !r.done || r.content == "" {
				t.Errorf("%s: картинку сервер понял, а клиент — ошибка %v, текст %q", s, r.err, r.content)
			}
			continue
		}
		if r.err == nil {
			t.Errorf("%s: код %d прошёл без ошибки", s, status)
			continue
		}
		if got := ollama.Retryable(r.err); got != (status >= 500) {
			t.Errorf("%s: код %d, повторяемая %v", s, status, got)
		}
		if !strings.Contains(r.err.Error(), "image") {
			t.Errorf("%s: в ошибке нет слов сервера о картинке: %v", s, r.err)
		}
	}
}

func TestRecordModels(t *testing.T) {
	want := map[string]map[string]int{
		"vllm":     {"qwen38": 32768},
		"sglang":   {"qwen38": 32768},
		"llamacpp": {"qwen38": 32768}, // meta.n_ctx, а не n_ctx_train 262144
	}
	litellm := map[string]int{"qwen38-raw": 32768, "chat": 32768,
		"chat-long": 131072, "chat-long-p1": 131072, "chat-long-p2": 131072}
	want["litellm-qwen38-raw"] = litellm
	want["litellm-chat"] = litellm
	for _, s := range recordServers {
		c := serveRecord(t, s, "models")
		models, err := c.Models(context.Background())
		if err != nil {
			t.Errorf("%s: %v", s, err)
			continue
		}
		got := map[string]int{}
		for _, m := range models {
			got[m.ID] = m.ContextLength
		}
		if len(got) != len(want[s]) {
			t.Errorf("%s: модели %v, ждали %v", s, got, want[s])
		}
		for id, n := range want[s] {
			if got[id] != n {
				t.Errorf("%s: окно %s = %d, ждали %d", s, id, got[id], n)
			}
		}
	}
}
