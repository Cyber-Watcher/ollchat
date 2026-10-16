package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
)

// Этап 112, Ц3 и Ц4: запись вызова в журнале несёт то, что «ok» скрывает, —
// пустой ответ, размер, и сколько вызовов шло одновременно.
func TestCallExtraLogged(t *testing.T) {
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	empty := Tool{
		Spec: ollama.ToolSpec{Name: "пусто", Description: "ничего не находит",
			Parameters: ollama.ToolParams{Type: "object"}},
		Run: func(context.Context, map[string]any) (string, error) { return "", nil },
	}
	s := server(probe(), empty)
	s.Steps = steplog.New(t.TempDir(), pat, time.Now(), "ollmcp", true)
	call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"проба","arguments":{"что":"кот"}}}`)
	call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"пусто","arguments":{}}}`)
	s.Steps.Close()
	data, err := os.ReadFile(s.Steps.Path())
	if err != nil {
		t.Fatal(err)
	}
	var tools []steplog.Step
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		st, err := steplog.Parse([]byte(line))
		if err != nil {
			t.Fatalf("строка %q: %v", line, err)
		}
		if st.Kind == steplog.KindTool {
			tools = append(tools, st)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("записей вызова %d, ждали 2", len(tools))
	}
	ok, none := tools[0].Extra, tools[1].Extra
	if ok["empty"] != false || ok["bytes"] != float64(len("нашлось: кот")) || ok["in_flight"] != float64(1) {
		t.Errorf("обычный ответ: %+v", ok)
	}
	if none["empty"] != true || none["truncated"] != false || none["clamped"] != false {
		t.Errorf("пустой ответ не помечен: %+v", none)
	}
	if callExtra(callResult("строка\n\n[вывод обрезан: показано 5 из 9 байт]", nil), 3)["truncated"] != true ||
		callExtra(callResult("ответ\n\n[служба: top_k 50 → 20]", nil), 1)["clamped"] != true {
		t.Error("обрезка или поправка службы не распознаны")
	}
	// Метки в середине ответа — цитата, а не признак (живая ложная «обрезка» 16.10.2026:
	// выдача поиска несла строку callExtra с меткой внутри).
	quoted := callExtra(callResult("выдержка: strings.Contains(text, \"[вывод обрезан:\")\n\n"+
		"[вывод обрезан: показано 1 из 2 байт] — так выглядит метка\n\n[служба: x] тоже\nконец", nil), 1)
	if quoted["truncated"] != false || quoted["clamped"] != false {
		t.Errorf("метка внутри текста принята за признак: %+v", quoted)
	}
	// Как ставит Policy.finish: приписка службы, затем обрезка по потолку — метка обрезки последней.
	p := Policy{OutputBytes: 40}
	cut := callExtra(callResult(p.finish(strings.Repeat("кот ", 30), []string{"top_k 50 → 20"}), nil), 1)
	if cut["truncated"] != true {
		t.Errorf("обрезка Policy.finish не распознана: %+v", cut)
	}
	// Обрезка внутри инструмента, затем приписка службы — оба признака.
	wide := Policy{OutputBytes: 4096}
	both := callExtra(callResult(wide.finish("строка\n\n[вывод обрезан: показано 6 из 90 байт]", []string{"top_k 50 → 20"}), nil), 1)
	if both["truncated"] != true || both["clamped"] != true {
		t.Errorf("обрезка инструмента с припиской службы: %+v", both)
	}
	if s.inFlight.Load() != 0 {
		t.Errorf("после вызовов в полёте %d, ждали 0", s.inFlight.Load())
	}
}

// Ц4: восемь вызовов, держащих друг друга, пока не начнутся все, — в журнале
// числа «в полёте» от 1 до 8, каждое по разу, а после них счётчик пуст.
func TestCallInFlightParallel(t *testing.T) {
	const n = 8
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var started atomic.Int64
	release := make(chan struct{})
	slow := Tool{
		Spec: ollama.ToolSpec{Name: "медленно", Description: "ждёт остальных",
			Parameters: ollama.ToolParams{Type: "object"}},
		Run: func(ctx context.Context, _ map[string]any) (string, error) {
			if started.Add(1) == n {
				close(release)
			}
			select {
			case <-release:
				return "готово", nil
			case <-time.After(10 * time.Second):
				return "", errors.New("остальные вызовы не пришли")
			}
		},
	}
	s := server(slow)
	s.Steps = steplog.New(t.TempDir(), pat, time.Now(), "ollmcp", true)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			s.Handle(context.Background(), fmt.Appendf(nil,
				`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"медленно","arguments":{}}}`, i+1))
		})
	}
	wg.Wait()
	s.Steps.Close()
	data, err := os.ReadFile(s.Steps.Path())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[float64]int{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		st, err := steplog.Parse([]byte(line))
		if err != nil {
			t.Fatalf("строка %q: %v", line, err)
		}
		if st.Kind == steplog.KindTool {
			if st.Outcome != steplog.OutcomeOK {
				t.Errorf("вызов не удался: %+v", st)
			}
			v, _ := st.Extra["in_flight"].(float64)
			seen[v]++
		}
	}
	for k := 1; k <= n; k++ {
		if seen[float64(k)] != 1 {
			t.Errorf("в полёте %d записано %d раз, ждали 1 (всё: %v)", k, seen[float64(k)], seen)
		}
	}
	if s.inFlight.Load() != 0 {
		t.Errorf("после вызовов в полёте %d, ждали 0", s.inFlight.Load())
	}
}

// В1: `_meta` вызова доходит до записи журнала — по нему запись службы
// связывается с вызовом у клиента; без `_meta` поля нет вовсе.
func TestCallClientMetaLogged(t *testing.T) {
	pat, err := chatlog.ParsePattern("steps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	s := server(probe())
	s.Steps = steplog.New(t.TempDir(), pat, time.Now(), "ollmcp", true)
	long := strings.Repeat("я", 100) // 200 байт: обрезается по границе буквы
	call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"проба","arguments":{"что":"кот"},`+
		`"_meta":{"client/callId":"call-7","progressToken":5,"nested":{"a":1},"long":"`+long+`"}}}`)
	call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"проба","arguments":{"что":"пёс"}}}`)
	s.Steps.Close()
	data, err := os.ReadFile(s.Steps.Path())
	if err != nil {
		t.Fatal(err)
	}
	var metas []any
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		st, err := steplog.Parse([]byte(line))
		if err != nil {
			t.Fatalf("строка %q: %v", line, err)
		}
		if st.Kind == steplog.KindTool {
			metas = append(metas, st.Extra["client_meta"])
		}
	}
	if len(metas) != 2 {
		t.Fatalf("записей вызова %d, ждали 2", len(metas))
	}
	m, _ := metas[0].(map[string]any)
	if m["client/callId"] != "call-7" || m["progressToken"] != float64(5) || m["nested"] != "{…}" {
		t.Errorf("метаданные клиента: %+v", m)
	}
	if got, _ := m["long"].(string); len(got) > metaMaxBytes+len("…") || !utf8.ValidString(got) {
		t.Errorf("длинная строка не обрезана или разрезана буква: %d байт", len(got))
	}
	if metas[1] != nil {
		t.Errorf("без _meta в записи %+v, ждали ничего", metas[1])
	}
}
