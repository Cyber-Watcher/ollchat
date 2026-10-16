package ollama

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Логарифмы вероятностей токенов ответа (этап 112, Ч15). Форма чанка — из живого
// ответа Ollama 0.32.13 на стенде: в чанке массив `logprobs` с токеном и логарифмом.
const logprobsFixture = `{"model":"m","created_at":"2026-10-14T18:51:31Z","message":{"role":"assistant","content":"","thinking":"думаю"},"logprobs":[{"token":"ду","logprob":-3.0},{"token":"маю","logprob":-2.0}],"done":false}
{"model":"m","created_at":"2026-10-14T18:51:31Z","message":{"role":"assistant","content":"Па"},"logprobs":[{"token":"П","logprob":-0.1},{"token":"а","logprob":-0.9}],"done":false}
{"model":"m","created_at":"2026-10-14T18:51:31Z","message":{"role":"assistant","content":"риж"},"logprobs":[{"token":"риж","logprob":-0.2}],"done":false}
{"model":"m","created_at":"2026-10-14T18:51:31Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":21,"eval_count":5}
`

func TestChatLogprobsSummary(t *testing.T) {
	c := serveFixture(t, logprobsFixture)
	var stats Stats
	for ev := range c.Chat(context.Background(), ChatRequest{Model: "m", Logprobs: true}) {
		switch ev.Kind {
		case EventDone:
			stats = ev.Stats
		case EventError:
			t.Fatalf("неожиданная ошибка: %v", ev.Err)
		}
	}
	// Токены рассуждения (-3, -2) в сводку не входят: считается текст ответа.
	if stats.LogprobN != 3 {
		t.Fatalf("токенов ответа с вероятностью: %d, ожидалось 3", stats.LogprobN)
	}
	if math.Abs(stats.LogprobSum-(-1.2)) > 1e-9 || stats.LogprobMin != -0.9 || stats.LogprobLow != 1 {
		t.Errorf("сумма %v, минимум %v, ниже 0,5: %d; ожидалось -1,2, -0,9 и 1",
			stats.LogprobSum, stats.LogprobMin, stats.LogprobLow)
	}
	if math.Abs(stats.MeanLogprob()-(-0.4)) > 1e-9 {
		t.Errorf("средний логарифм %v, ожидалось -0,4", stats.MeanLogprob())
	}
	// Счётчики финального чанка не затёрты сводкой.
	if stats.PromptEvalCount != 21 || stats.EvalCount != 5 || stats.DoneReason != "stop" {
		t.Errorf("счётчики финального чанка потеряны: %+v", stats)
	}
}

func TestChatWithoutLogprobsHasNoSummary(t *testing.T) {
	c := serveFixture(t, streamFixture)
	for ev := range c.Chat(context.Background(), ChatRequest{Model: "m"}) {
		if ev.Kind == EventDone && (ev.Stats.LogprobN != 0 || ev.Stats.MeanLogprob() != 0) {
			t.Errorf("без logprobs сводки быть не должно: %+v", ev.Stats)
		}
	}
}

// Ключ `logprobs` уходит на сервер только по просьбе: в работе запрос прежний.
func TestChatRequestLogprobsFlag(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		bodies = append(bodies, m)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}` + "\n"))
	}))
	defer srv.Close()
	c := New(srv.URL, 10*time.Second, 0, nil)
	for _, req := range []ChatRequest{{Model: "m"}, {Model: "m", Logprobs: true}} {
		for range c.Chat(context.Background(), req) {
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("запросов на сервер: %d, ожидалось 2", len(bodies))
	}
	if _, has := bodies[0]["logprobs"]; has {
		t.Errorf("без просьбы ключ logprobs не должен уходить: %v", bodies[0])
	}
	if bodies[1]["logprobs"] != true {
		t.Errorf("с просьбой ключ logprobs должен быть true: %v", bodies[1])
	}
}
