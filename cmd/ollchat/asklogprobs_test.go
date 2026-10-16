package main

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/mixer"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// --logprobs уходит на сервер, а сводка по токенам ответа попадает в строку
// JSON и в «чем считали» (этап 112, Ч15). Без ключа ни просьбы, ни полей нет.
func TestAskLogprobsReachJSON(t *testing.T) {
	var asked []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		want := req["logprobs"] == true
		asked = append(asked, want)
		// Настоящий сервер отдаёт токены только тому, кто просил.
		lp := ""
		if want {
			lp = `"logprobs":[{"token":"Па","logprob":-0.1},{"token":"риж","logprob":-0.9}],`
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"Париж"},` + lp + `"done":false}` + "\n" +
			`{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":2}` + "\n"))
	}))
	defer srv.Close()

	cfg := config.Default()
	cs := &config.Server{Name: "проба", URL: srv.URL}
	client := ollama.New(srv.URL, 10*time.Second, 0, nil)

	with := askOpts{Mix: "off", Logprobs: true}
	ans := askOne(client, cfg, cs, "m", "столица?", with, mixer.Settings{}, nil, nil)
	if ans.Error != "" {
		t.Fatalf("ошибка ответа: %s", ans.Error)
	}
	if ans.LogprobTokens != 2 || ans.LogprobLow != 1 || ans.LogprobMin != -0.9 ||
		math.Abs(ans.LogprobMean-(-0.5)) > 1e-9 {
		t.Errorf("сводка: токенов %d, средний %v, минимум %v, ниже 0,5: %d; ждали 2, -0,5, -0,9, 1",
			ans.LogprobTokens, ans.LogprobMean, ans.LogprobMin, ans.LogprobLow)
	}
	if ans.Settings["logprobs"] != true {
		t.Errorf("в settings нет logprobs: %v", ans.Settings)
	}

	without := askOpts{Mix: "off"}
	ans = askOne(client, cfg, cs, "m", "столица?", without, mixer.Settings{}, nil, nil)
	if ans.LogprobTokens != 0 || ans.Settings["logprobs"] != nil {
		t.Errorf("без ключа сводки быть не должно: %+v", ans)
	}
	if len(asked) != 2 || !asked[0] || asked[1] {
		t.Errorf("просьба logprobs на сервер: %v, ждали [true false]", asked)
	}
}
