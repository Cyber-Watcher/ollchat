package graphex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// сервер изображает Ollama: отдаёт поток чанков и запоминает запрос.
func server(t *testing.T, answer string, failuresInRow *int32) (*httptest.Server, *map[string]any) {
	t.Helper()
	last := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &last)
		if failuresInRow != nil && atomic.AddInt32(failuresInRow, -1) >= 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"503 model is loading"}`))
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		for _, part := range strings.Split(answer, "|") {
			_ = enc.Encode(map[string]any{
				"model": "проба", "message": map[string]any{"role": "assistant", "content": part},
				"done": false,
			})
		}
		_ = enc.Encode(map[string]any{"model": "проба", "done": true,
			"eval_count": 42, "prompt_eval_count": 7})
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

func opts(url string) Options {
	return Options{Model: "проба", URL: url, NumCtx: 4096, Temperature: 0.2, KeepAlive: "30m"}
}

// Извлекатель собирает поток в ответ.
func TestExtractorJoinsStream(t *testing.T) {
	srv, request := server(t, `{"entities":[|{"name":"Go","type":"технология"}]}`, nil)
	ex := New(opts(srv.URL), "", 30*time.Second, nil)
	if ex == nil {
		t.Fatal("извлекатель не собрался")
	}

	got, err := ex.Extract(context.Background(), "система", "вопрос")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"name":"Go"`) {
		t.Errorf("ответ собран неверно: %q", got)
	}

	// Рассуждения обязаны быть выключены: на извлечении они бесполезны,
	// а стоят десятков тысяч знаков — это замерено на прогонах моделей.
	if think, ok := (*request)["think"].(bool); !ok || think {
		t.Errorf("think = %v, ожидалось false", (*request)["think"])
	}
	opts, _ := (*request)["options"].(map[string]any)
	if opts["num_ctx"] != float64(4096) {
		t.Errorf("num_ctx = %v", opts["num_ctx"])
	}
	if opts["temperature"] != 0.2 {
		t.Errorf("temperature = %v", opts["temperature"])
	}
	if (*request)["keep_alive"] != "30m" {
		t.Errorf("keep_alive = %v", (*request)["keep_alive"])
	}
}

// Сборка идёт часами: одна оборванная связь не должна её ронять.
func TestRetryAfterServerFailure(t *testing.T) {
	failures := int32(2)
	srv, _ := server(t, `{"entities":[]}`, &failures)
	ex := New(opts(srv.URL), "", 30*time.Second, nil)

	got, err := ex.Extract(context.Background(), "система", "вопрос")
	if err != nil {
		t.Fatalf("не пережил двух сбоев подряд: %v", err)
	}
	if !strings.Contains(got, "entities") {
		t.Errorf("ответ = %q", got)
	}
}

// Без модели извлекатель не собирается.
func TestNoModelNoExtractor(t *testing.T) {
	if ex := New(Options{}, "http://127.0.0.1:1", time.Second, nil); ex != nil {
		t.Error("извлекатель собрался без заданной модели")
	}
}

// Пустой ответ это ошибка.
func TestEmptyAnswerIsError(t *testing.T) {
	srv, _ := server(t, "", nil)
	ex := New(opts(srv.URL), "", 5*time.Second, nil)
	if _, err := ex.Extract(context.Background(), "с", "в"); err == nil {
		t.Error("пустой ответ принят за годный")
	}
}
