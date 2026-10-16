package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/mixer"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
)

// Этап 112, В1: у каждого --ask свой номер хода. С константой «ask» два запуска в
// одну секунду писали в один файл журнала, и stepstat.py, группирующий записи по
// (файл, turn), видел вместо двух ходов один.
func TestAskTurnIDsDiffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"да"},"done":false}` + "\n" +
			`{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":2}` + "\n"))
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.Log.Enabled = true
	cfg.Log.Dir = t.TempDir()
	cs := &config.Server{Name: "проба", URL: srv.URL}
	client := ollama.New(srv.URL, 10*time.Second, 0, nil)
	for range 2 {
		if ans := askOne(client, cfg, cs, "m", "вопрос?", askOpts{Mix: "off"}, mixer.Settings{}, nil, nil); ans.Error != "" {
			t.Fatalf("ошибка ответа: %s", ans.Error)
		}
	}

	files, _ := filepath.Glob(filepath.Join(cfg.Log.Dir, "steps-*.jsonl"))
	turns := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
			st, err := steplog.Parse([]byte(line))
			if err != nil {
				t.Fatalf("строка %q: %v", line, err)
			}
			if st.Kind == steplog.KindTurn {
				if st.Turn == "" || st.Turn == "ask" {
					t.Errorf("номер хода %q — не идентификатор", st.Turn)
				}
				turns[st.Turn] = true
			}
		}
	}
	if len(turns) != 2 {
		t.Errorf("разных номеров хода %d, ждали 2 (%v)", len(turns), turns)
	}
}
