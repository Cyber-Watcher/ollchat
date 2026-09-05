package session

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

func TestTodayLine(t *testing.T) {
	got := TodayLine(time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC))
	if !strings.HasPrefix(got, "Сегодня 23 августа 2026 года.") {
		t.Fatalf("дата написана не по-русски: %q", got)
	}
	if !strings.Contains(got, "не из памяти") {
		t.Fatalf("нет оговорки о пределе знаний модели: %q", got)
	}
}

// TestTodayInRequest — дата идёт первой, системный промпт пользователя следом
// и целиком, а сохранённая сессия про дату ничего не знает.
func TestTodayInRequest(t *testing.T) {
	c := New("Ты — помощник-программист.")
	c.Append(ollama.Message{Role: ollama.RoleUser, Content: "какой сейчас год"})

	if req := c.Request(); req[0].Role != ollama.RoleSystem ||
		strings.Contains(req[0].Content, "Сегодня") {
		t.Fatalf("дата подставилась без настройки: %q", req[0].Content)
	}

	c.SetToday(true)
	req := c.Request()
	if req[0].Role != ollama.RoleSystem {
		t.Fatalf("первое сообщение не системное: %+v", req[0])
	}
	if !strings.HasPrefix(req[0].Content, "Сегодня ") {
		t.Fatalf("дата не первая: %q", req[0].Content)
	}
	if !strings.Contains(req[0].Content, "Ты — помощник-программист.") {
		t.Fatalf("промпт пользователя потерялся: %q", req[0].Content)
	}
	if len(req) != 2 {
		t.Fatalf("история испорчена: %d сообщений", len(req))
	}

	// В сохранённую сессию дата не попадает: разговор, сохранённый вчера,
	// не должен утверждать, что сегодня вчера.
	if s := c.System(); strings.Contains(s, "Сегодня") {
		t.Fatalf("дата просочилась в системный промпт: %q", s)
	}
	store := NewStore(t.TempDir())
	path, err := store.Save(c, "srv", "model")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Сегодня ") {
		t.Fatalf("дата попала в сохранённую сессию: %s", data)
	}

	// Пустой промпт пользователя не мешает дате быть.
	c2 := New("")
	c2.SetToday(true)
	c2.Append(ollama.Message{Role: ollama.RoleUser, Content: "привет"})
	if req := c2.Request(); len(req) != 2 || !strings.HasPrefix(req[0].Content, "Сегодня ") {
		t.Fatalf("без промпта пользователя дата не ушла: %+v", req)
	}
}

// TestRequestAtStableWithinTurn — одна и та же дата даёт одно и то же
// системное сообщение, сколько бы раз ни собирался запрос за ход.
func TestRequestAtStableWithinTurn(t *testing.T) {
	c := New("Ты помощник.")
	c.SetToday(true)
	c.Append(ollama.Message{Role: ollama.RoleUser, Content: "привет"})
	now := time.Date(2026, 9, 3, 23, 59, 59, 0, time.Local)
	a := c.RequestAt(now)
	b := c.RequestAt(now)
	if a[0].Content != b[0].Content {
		t.Fatalf("системное сообщение разошлось:\n%s\n%s", a[0].Content, b[0].Content)
	}
	next := c.RequestAt(now.Add(2 * time.Second))
	if next[0].Content == a[0].Content {
		t.Fatal("после полуночи дата в системном сообщении должна смениться")
	}
}
