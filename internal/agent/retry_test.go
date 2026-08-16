package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/session"
)

// flakyServer изображает сервер, который на первые failTimes запросов срывает
// генерацию так же, как это делает Ollama: ответ 200, затем чанк с полем error.
// Именно так выглядел сбой «XML syntax error ... unexpected EOF» на стенде.
func flakyServer(t *testing.T, failTimes int, thinkBeforeFail bool, contentBeforeFail bool) (*ollama.Client, *int32) {
	t.Helper()
	var calls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&calls, 1))
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		flusher, _ := w.(http.Flusher)

		if n <= failTimes {
			if thinkBeforeFail {
				_ = enc.Encode(ollama.ChatResponse{Message: ollama.Message{
					Role: ollama.RoleAssistant, Thinking: "рассуждаю…"}})
			}
			if contentBeforeFail {
				_ = enc.Encode(ollama.ChatResponse{Message: ollama.Message{
					Role: ollama.RoleAssistant, Content: "начало ответа"}})
			}
			if flusher != nil {
				flusher.Flush()
			}
			_ = enc.Encode(ollama.ChatResponse{Error: "XML syntax error on line 15: unexpected EOF"})
			return
		}

		_ = enc.Encode(ollama.ChatResponse{Message: ollama.Message{
			Role: ollama.RoleAssistant, Content: "готовый ответ"}})
		_ = enc.Encode(ollama.ChatResponse{Done: true, DoneReason: "stop",
			PromptEvalCount: 10, EvalCount: 5})
	}))
	t.Cleanup(srv.Close)

	return ollama.New(srv.URL, 10*time.Second, nil), &calls
}

func plainRunner(client *ollama.Client, retries int) *Runner {
	return &Runner{Client: client, Model: "test", MaxIterations: 5, MaxRetries: retries}
}

func collect(t *testing.T, r *Runner) (answer string, retries int, err error) {
	t.Helper()
	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "вопрос"})

	var sb strings.Builder
	for ev := range r.Run(context.Background(), conv) {
		switch ev.Kind {
		case EventContent:
			sb.WriteString(ev.Text)
		case EventRetry:
			retries++
		case EventError:
			err = ev.Err
		}
	}
	return sb.String(), retries, err
}

func TestRetryRecoversFromServerFailure(t *testing.T) {
	client, calls := flakyServer(t, 2, true, false)
	answer, retries, err := collect(t, plainRunner(client, 2))

	if err != nil {
		t.Fatalf("после повторов ошибки быть не должно: %v", err)
	}
	if answer != "готовый ответ" {
		t.Errorf("ответ = %q, ожидался %q", answer, "готовый ответ")
	}
	if retries != 2 {
		t.Errorf("сообщений о повторе = %d, ожидалось 2", retries)
	}
	if n := atomic.LoadInt32(calls); n != 3 {
		t.Errorf("обращений к серверу = %d, ожидалось 3", n)
	}
}

func TestRetryGivesUpAfterLimit(t *testing.T) {
	client, calls := flakyServer(t, 10, true, false)
	_, _, err := collect(t, plainRunner(client, 2))

	if err == nil {
		t.Fatal("если повторы не помогли, ошибка должна дойти до пользователя")
	}
	if !strings.Contains(err.Error(), "XML syntax error") {
		t.Errorf("текст ошибки сервера должен сохраняться: %v", err)
	}
	if !strings.Contains(err.Error(), "после 3 попыток") {
		t.Errorf("в ошибке должно быть число попыток: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 3 {
		t.Errorf("обращений к серверу = %d, ожидалось 3 (1 + 2 повтора)", n)
	}
}

// Ключевое ограничение: если часть ответа уже показана пользователю,
// повторять запрос нельзя — текст пришёл бы дважды.
func TestNoRetryAfterAnswerStarted(t *testing.T) {
	client, calls := flakyServer(t, 10, true, true)
	answer, retries, err := collect(t, plainRunner(client, 3))

	if retries != 0 {
		t.Errorf("после начала ответа повторов быть не должно, получено %d", retries)
	}
	if err == nil {
		t.Fatal("ошибка должна дойти до пользователя")
	}
	if answer != "начало ответа" {
		t.Errorf("частичный ответ должен сохраниться: %q", answer)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("обращений к серверу = %d, ожидалось 1", n)
	}
}

func TestRetryDisabledByConfig(t *testing.T) {
	client, calls := flakyServer(t, 10, false, false)
	_, retries, err := collect(t, plainRunner(client, 0))

	if retries != 0 {
		t.Errorf("при max_retries = 0 повторов быть не должно, получено %d", retries)
	}
	if err == nil {
		t.Fatal("ошибка должна дойти до пользователя")
	}
	if strings.Contains(err.Error(), "попыток") {
		t.Errorf("без повторов не нужно писать про попытки: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("обращений к серверу = %d, ожидалось 1", n)
	}
}

// Ошибки 4xx повторять бессмысленно: запрос не станет верным сам собой.
func TestNoRetryOnClientError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model 'nope' not found"}`))
	}))
	defer srv.Close()

	client := ollama.New(srv.URL, 10*time.Second, nil)
	_, retries, err := collect(t, plainRunner(client, 3))

	if retries != 0 {
		t.Errorf("ошибку 404 повторять не нужно, повторов %d", retries)
	}
	if err == nil {
		t.Fatal("ошибка должна дойти до пользователя")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("обращений к серверу = %d, ожидалось 1", n)
	}
}

func TestRetryOnServerError5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal"}`))
			return
		}
		enc := json.NewEncoder(w)
		_ = enc.Encode(ollama.ChatResponse{Message: ollama.Message{
			Role: ollama.RoleAssistant, Content: "ответ"}})
		_ = enc.Encode(ollama.ChatResponse{Done: true, DoneReason: "stop"})
	}))
	defer srv.Close()

	client := ollama.New(srv.URL, 10*time.Second, nil)
	answer, retries, err := collect(t, plainRunner(client, 2))

	if err != nil {
		t.Fatalf("ошибка 5xx должна быть пережита повтором: %v", err)
	}
	if retries != 1 || answer != "ответ" {
		t.Errorf("повторов = %d, ответ = %q", retries, answer)
	}
}

func TestCancelIsNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		enc := json.NewEncoder(w)
		flusher, _ := w.(http.Flusher)
		// Медленно тянем ответ, пока клиент не уйдёт: обработчик обязан
		// завершиться сам, иначе httptest.Server не закроется.
		for i := 0; i < 100; i++ {
			if err := enc.Encode(ollama.ChatResponse{Message: ollama.Message{
				Role: ollama.RoleAssistant, Thinking: "думаю…"}}); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()

	client := ollama.New(srv.URL, 10*time.Second, nil)
	r := plainRunner(client, 3)

	conv := session.New("")
	conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "вопрос"})

	ctx, cancel := context.WithCancel(context.Background())
	events := r.Run(ctx, conv)
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	retries := 0
	for ev := range events {
		if ev.Kind == EventRetry {
			retries++
		}
	}
	if retries != 0 {
		t.Errorf("прерывание пользователем повторять нельзя, повторов %d", retries)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("обращений к серверу = %d, ожидалось 1", n)
	}
}
