package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

type scriptedChat struct {
	events []ollama.Event
	seen   ollama.ChatRequest
}

func (s *scriptedChat) Chat(_ context.Context, req ollama.ChatRequest) <-chan ollama.Event {
	s.seen = req
	ch := make(chan ollama.Event, len(s.events)+1)
	for _, ev := range s.events {
		ch <- ev
	}
	close(ch)
	return ch
}

func history() []ollama.Message {
	return []ollama.Message{
		{Role: ollama.RoleUser, Content: "как называется файл настроек?"},
		{Role: ollama.RoleAssistant, Content: "config.toml в ~/.config/ollchat", ToolCalls: []ollama.ToolCall{{
			Function: ollama.ToolCallFunc{Name: "read_file", Arguments: map[string]any{"path": "x"}}}}},
		{Role: ollama.RoleTool, ToolName: "read_file", Content: "содержимое"},
		{Role: ollama.RoleAssistant, Content: "готово"},
		{Role: ollama.RoleUser, Content: "а порт?"},
		{Role: ollama.RoleAssistant, Content: "11434"},
	}
}

// Сжиматель получает системный промпт и расшифровку истории с ролями и
// вызовами; рассуждения выключены; ответ — сводка без обёртки.
func TestSummarizeSendsTranscript(t *testing.T) {
	cl := &scriptedChat{events: []ollama.Event{
		{Kind: ollama.EventContent, Text: "- файл настроек: config.toml\n"},
		{Kind: ollama.EventContent, Text: "- порт 11434"},
		{Kind: ollama.EventDone, Stats: ollama.Stats{PromptEvalCount: 120, EvalCount: 20}},
	}}
	sum, stats, err := Summarize(context.Background(), cl, "m", history())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sum, "config.toml") || stats.EvalCount != 20 {
		t.Fatalf("сводка %q, статистика %+v", sum, stats)
	}
	if cl.seen.Model != "m" || cl.seen.Think == nil || *cl.seen.Think {
		t.Fatalf("запрос: модель %q, think %v", cl.seen.Model, cl.seen.Think)
	}
	if cl.seen.Messages[0].Role != ollama.RoleSystem || cl.seen.Messages[0].Content != CompactPrompt {
		t.Fatal("первым должен идти промпт сжимателя")
	}
	body := cl.seen.Messages[1].Content
	for _, want := range []string{"Человек: как называется", "Ассистент: config.toml", "[вызов read_file(", "Результат инструмента read_file: содержимое", "а порт?"} {
		if !strings.Contains(body, want) {
			t.Errorf("в расшифровке нет %q:\n%s", want, body)
		}
	}
	if len(CompactPromptID) != 8 {
		t.Fatalf("версия промпта: %q", CompactPromptID)
	}
}

func TestSummarizeFailures(t *testing.T) {
	if _, _, err := Summarize(context.Background(), &scriptedChat{}, "m", nil); err == nil {
		t.Fatal("пустая история должна быть ошибкой")
	}
	boom := &scriptedChat{events: []ollama.Event{{Kind: ollama.EventError, Err: errors.New("сервер занят")}}}
	if _, _, err := Summarize(context.Background(), boom, "m", history()); err == nil || !strings.Contains(err.Error(), "занят") {
		t.Fatalf("ошибка сервера должна доходить: %v", err)
	}
	empty := &scriptedChat{events: []ollama.Event{{Kind: ollama.EventDone}}}
	if _, _, err := Summarize(context.Background(), empty, "m", history()); err == nil {
		t.Fatal("пустая сводка должна быть ошибкой")
	}
	cut := &scriptedChat{events: []ollama.Event{{Kind: ollama.EventContent, Text: "нача"}}}
	if _, _, err := Summarize(context.Background(), cut, "m", history()); err == nil {
		t.Fatal("оборванный поток должен быть ошибкой")
	}
}

// Older и CompactWith делят историю по той же границе, что Compact: хвост
// не начинается с ответа инструмента; сводка становится первым сообщением.
func TestOlderAndCompactWith(t *testing.T) {
	c := New("")
	for _, m := range history() {
		c.Append(m)
	}
	// keep=4 началось бы с RoleTool (индекс 2) — граница сдвигается на вызов.
	older := c.Older(4)
	if len(older) != 1 || older[0].Content != "как называется файл настроек?" {
		t.Fatalf("в сводку должно уйти одно первое сообщение: %+v", older)
	}
	dropped := c.CompactWith(4, "сводка: файл config.toml")
	if dropped != 1 || c.Len() != 6 {
		t.Fatalf("сжато %d, осталось %d; ожидалось 1 и 6", dropped, c.Len())
	}
	first := c.Messages()[0]
	if first.Role != ollama.RoleUser || !strings.Contains(first.Content, "Сводка предыдущей части") || !strings.Contains(first.Content, "config.toml") {
		t.Fatalf("первое сообщение — не сводка: %+v", first)
	}
	if c.Messages()[1].Role != ollama.RoleAssistant {
		t.Fatalf("после сводки должен идти вызов инструмента, а не его ответ: %+v", c.Messages()[1])
	}
	if c.CompactWith(10, "ещё") != 0 {
		t.Fatal("короткая история не сжимается")
	}
}
