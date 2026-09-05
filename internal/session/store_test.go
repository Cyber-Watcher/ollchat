package session

import (
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

func msgs(roles ...string) []ollama.Message {
	out := make([]ollama.Message, 0, len(roles))
	for i, r := range roles {
		out = append(out, ollama.Message{Role: r, Content: string(rune('a' + i))})
	}
	return out
}

// Compact не оставляет историю, начинающуюся с ответа инструмента без
// запроса: окно расширяется до сообщения модели, которое его вызвало.
func TestCompactKeepsToolCallPair(t *testing.T) {
	c := New("")
	for _, m := range msgs(ollama.RoleUser, ollama.RoleAssistant, ollama.RoleTool, ollama.RoleAssistant, ollama.RoleUser, ollama.RoleAssistant) {
		c.Append(m)
	}
	// Окно в четыре сообщения началось бы с RoleTool — оно растёт до пяти.
	if dropped := c.Compact(4); dropped != 1 || c.Len() != 5 {
		t.Fatalf("сброшено %d, осталось %d; ожидалось 1 и 5", dropped, c.Len())
	}
	if c.Messages()[0].Role != ollama.RoleAssistant {
		t.Fatalf("история начинается с %s", c.Messages()[0].Role)
	}
	// Окно в три начинается с обычного ответа модели — как просили.
	if dropped := c.Compact(3); dropped != 2 || c.Len() != 3 {
		t.Fatalf("сброшено %d, осталось %d; ожидалось 2 и 3", dropped, c.Len())
	}
}

// keep = 0 сбрасывает всё и не падает; пустая история ничего не сбрасывает.
func TestCompactEdges(t *testing.T) {
	c := New("")
	if c.Compact(3) != 0 {
		t.Fatal("пустая история: нечего сбрасывать")
	}
	for _, m := range msgs(ollama.RoleUser, ollama.RoleAssistant, ollama.RoleTool) {
		c.Append(m)
	}
	if dropped := c.Compact(0); dropped != 3 || c.Len() != 0 {
		t.Fatalf("keep=0: сброшено %d, осталось %d", dropped, c.Len())
	}
	c.Append(ollama.Message{Role: ollama.RoleUser, Content: "x"})
	if dropped := c.Compact(-5); dropped != 1 || c.Len() != 0 {
		t.Fatalf("keep<0 равносильно нулю: сброшено %d, осталось %d", dropped, c.Len())
	}
}

// Messages и Request отдают копии: правка снаружи историю не трогает.
func TestMessagesAreCopies(t *testing.T) {
	c := New("система")
	c.Append(ollama.Message{Role: ollama.RoleUser, Content: "вопрос"})
	got := c.Messages()
	got[0].Content = "подмена"
	if c.Messages()[0].Content != "вопрос" {
		t.Fatal("Messages вернул ссылку на историю, а не копию")
	}
	req := c.Request()
	if len(req) != 2 || req[0].Role != ollama.RoleSystem || req[0].Content != "система" {
		t.Fatalf("запрос без системного сообщения: %+v", req)
	}
	req[1].Content = "подмена"
	if c.Messages()[0].Content != "вопрос" {
		t.Fatal("Request вернул ссылку на историю, а не копию")
	}
}

// Сохранение и восстановление по кругу; список — от свежих к старым;
// битый файл список не ломает.
func TestStoreRoundTrip(t *testing.T) {
	st := NewStore(t.TempDir())
	c := New("система")
	c.Append(ollama.Message{Role: ollama.RoleUser, Content: "первый"})
	c.Append(ollama.Message{Role: ollama.RoleAssistant, Content: "ответ"})
	path, err := st.Save(c, "local", "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("пустой путь сохранения")
	}

	list, err := st.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("список: %v, %d", err, len(list))
	}
	rec, err := st.Load(list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Model != "test-model" || rec.Server != "local" || rec.System != "система" || len(rec.Messages) != 2 {
		t.Fatalf("восстановлено не то: %+v", rec)
	}
	latest, err := st.Load("")
	if err != nil || latest.ID != rec.ID {
		t.Fatalf("пустой id должен давать свежую сессию: %v", err)
	}

	fresh := New("")
	fresh.Restore(rec)
	if fresh.System() != "система" || fresh.Len() != 2 {
		t.Fatalf("Restore: system=%q, len=%d", fresh.System(), fresh.Len())
	}

	if _, err := st.Load("нет-такой"); err == nil {
		t.Fatal("несуществующая сессия должна давать ошибку")
	}
}
