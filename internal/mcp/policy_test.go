package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// counted — инструмент с числовым параметром «сколько»: возвращает, что получил.
func counted(seen *any) Tool {
	return Tool{
		Spec: ollama.ToolSpec{
			Name:        "счёт",
			Description: "проба пределов",
			Parameters: ollama.ToolParams{Type: "object", Properties: map[string]ollama.ToolProp{
				"сколько": {Type: "integer", Description: "Сколько вернуть, 1..20"},
			}},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			*seen = args["сколько"]
			return strings.Repeat("я", 100), nil
		},
	}
}

func limited(max int) Policy {
	return Policy{Limits: map[string]map[string]Limit{"счёт": {"сколько": {Min: 1, Max: max}}}}
}

func callText(t *testing.T, s *Server, args string) (string, bool) {
	t.Helper()
	d := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"счёт","arguments":`+args+`}}`)
	r := d["result"].(map[string]any)
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	isErr, _ := r["isError"].(bool)
	return text, isErr
}

// Описание параметра показывает тот диапазон, который служба соблюдает.
func TestPolicyDescribesLimits(t *testing.T) {
	var seen any
	s := server(counted(&seen))
	s.Policy = limited(8)
	d := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tool := d["result"].(map[string]any)["tools"].([]any)[0].(map[string]any)
	prop := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)["сколько"].(map[string]any)
	if prop["description"] != "Сколько вернуть, 1..8" {
		t.Errorf("описание = %q", prop["description"])
	}
	if describe("без диапазона", Limit{0, 5}) != "без диапазона (0..5)" {
		t.Error("диапазон не дописан к описанию без чисел")
	}
	// Смена предела меняет отпечаток: клиент получит уведомление.
	other := server(counted(&seen))
	other.Policy = limited(9)
	if s.Fingerprint() == other.Fingerprint() {
		t.Error("смена предела не изменила отпечаток набора")
	}
}

// Каждая поправка видна в ответе, а инструмент получает исправленное значение.
func TestPolicyAdjustsWithNote(t *testing.T) {
	var seen any
	s := server(counted(&seen))
	s.Policy = limited(8)

	cases := []struct {
		args string
		want any
		note string
	}{
		{`{"сколько":50}`, float64(8), "больше предела службы 8 — взято 8"},
		{`{"сколько":0}`, nil, "меньше 1 — взято умолчание"},
		{`{"сколько":"много"}`, nil, "ждали целое число"},
		{`{"сколько":2.5}`, nil, "ждали целое число"},
		{`{"сколько":"5"}`, "5", ""},
		{`{"сколько":3}`, float64(3), ""},
	}
	for _, c := range cases {
		seen = "не звали"
		text, isErr := callText(t, s, c.args)
		if isErr {
			t.Errorf("%s: ошибка вместо ответа: %s", c.args, text)
		}
		if seen != c.want {
			t.Errorf("%s: инструмент получил %#v, ждали %#v", c.args, seen, c.want)
		}
		if c.note == "" && strings.Contains(text, "[служба:") {
			t.Errorf("%s: лишняя пометка: %s", c.args, text)
		}
		if c.note != "" && !strings.Contains(text, c.note) {
			t.Errorf("%s: нет пометки %q в %q", c.args, c.note, text)
		}
	}
}

// Потолок ответа режет по границе символа и говорит об этом.
func TestPolicyOutputCeiling(t *testing.T) {
	var seen any
	s := server(counted(&seen))
	s.Policy = Policy{OutputBytes: 51} // 25,5 знака по 2 байта
	text, _ := callText(t, s, `{}`)
	if !strings.HasPrefix(text, strings.Repeat("я", 25)+"\n") || !strings.Contains(text, "вывод обрезан") {
		t.Errorf("обрезка: %q", text)
	}
}

// Истёк общий срок — ответ «не успел» с подсказкой, а не зависание.
func TestPolicyCallTimeout(t *testing.T) {
	slow := Tool{
		Spec: ollama.ToolSpec{Name: "счёт", Description: "медленный",
			Parameters: ollama.ToolParams{Type: "object", Properties: map[string]ollama.ToolProp{}}},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(5 * time.Second):
				return "успел", nil
			}
		},
	}
	s := server(slow)
	s.Policy = Policy{CallTimeout: 50 * time.Millisecond}
	started := time.Now()
	text, isErr := callText(t, s, `{}`)
	if !isErr || !strings.Contains(text, "не успела за 50ms") || !strings.Contains(text, "прогревается") {
		t.Errorf("ответ по сроку: %v %q", isErr, text)
	}
	if time.Since(started) > 2*time.Second {
		t.Errorf("срок не сработал: %s", time.Since(started))
	}
}
