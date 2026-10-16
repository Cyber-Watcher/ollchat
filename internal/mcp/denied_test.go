package mcp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
)

// С2: отказ входа без ключа ложится в журнал шагов — одной записью на адрес
// в минуту, с числом пропущенных между записями; запрос с ключом — не отказ.
func TestDeniedWithoutKeyLogged(t *testing.T) {
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	s := server(probe())
	s.Steps = steplog.New(t.TempDir(), pat, time.Now(), "ollmcp", true)
	mux := http.NewServeMux()
	MountHTTP(mux, s, "секрет", false)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	post := func(key string) int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	for range 3 {
		if c := post(""); c != http.StatusUnauthorized {
			t.Fatalf("без ключа %d, ждали 401", c)
		}
	}
	if c := post("секрет"); c != http.StatusOK {
		t.Fatalf("с ключом %d, ждали 200", c)
	}
	// Минута прошла — следующий отказ пишется и несёт число пропущенных.
	s.mu.Lock()
	s.denied["127.0.0.1"].at = time.Now().Add(-2 * deniedEvery)
	s.mu.Unlock()
	post("")
	s.Steps.Close()
	data, err := os.ReadFile(s.Steps.Path())
	if err != nil {
		t.Fatal(err)
	}
	var denied []steplog.Step
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		st, err := steplog.Parse([]byte(line))
		if err != nil {
			t.Fatalf("строка %q: %v", line, err)
		}
		if st.Outcome == steplog.OutcomeDenied {
			if st.Kind != steplog.KindAuth || st.Tool != "" {
				t.Errorf("отказ без ключа записан как %q с инструментом %q, ждали %q без имени", st.Kind, st.Tool, steplog.KindAuth)
			}
			denied = append(denied, st)
		}
	}
	if len(denied) != 2 {
		t.Fatalf("записей отказа %d, ждали 2 (первая и после минуты)", len(denied))
	}
	if denied[0].Extra["remote"] != "127.0.0.1" || denied[0].Extra["skipped"] != float64(0) {
		t.Errorf("первая запись: %+v", denied[0].Extra)
	}
	if denied[1].Extra["skipped"] != float64(2) {
		t.Errorf("пропущено между записями %v, ждали 2", denied[1].Extra["skipped"])
	}
}
