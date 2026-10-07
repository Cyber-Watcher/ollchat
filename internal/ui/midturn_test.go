package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/session"
)

// Фоновые команды отвечают когда успеют, а человек тем временем задаёт
// вопрос. Ответ, пришедший посреди хода, не должен ни ронять программу,
// ни ломать историю, которую в это время дописывает агент.

func streamText(m *Model, text string) {
	m.Update(agentEventMsg{gen: m.gen.run, ev: agent.Event{Kind: agent.EventContent, Text: text}})
}

// /resume дочитался, когда модель уже отвечала: лента заменялась, индекс
// живого блока смотрел за её конец, и следующий кусок ответа ронял
// программу с index out of range — диалог пропадал (аудит 07.10.2026).
func TestResumeMidTurnDoesNotReplaceDialog(t *testing.T) {
	m := newTestModel(t)
	m.addBlock(block{kind: blockUser, text: "вопрос о горутинах"})
	m.conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "вопрос о горутинах"})
	stuckTurn(m)
	streamText(m, "начало ответа")

	m.Update(sessionLoadedMsg{rec: &session.Saved{ID: "20261006-090000",
		SavedAt: time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC),
		Messages: []ollama.Message{{Role: ollama.RoleUser, Content: "старый вопрос"},
			{Role: ollama.RoleAssistant, Content: "старый ответ"}}}})
	streamText(m, " и его продолжение")

	if got := m.blocks[m.liveIdx].text; got != "начало ответа и его продолжение" {
		t.Errorf("ответ разорван: %q", got)
	}
	for _, msg := range m.conv.Messages() {
		if strings.Contains(msg.Content, "старый") {
			t.Fatal("история подменена посреди хода")
		}
	}
	var refused bool
	for _, b := range m.blocks {
		if b.kind == blockError && strings.Contains(b.text, "/resume 20261006-090000") {
			refused = true
		}
	}
	if !refused {
		t.Error("человеку не сказано, что сессия не восстановлена и как повторить")
	}
}

// /add дочитал файл, когда агент уже вызвал инструмент: вложение вставало
// между вызовом и его результатом, а такой порядок сервер не принимает.
func TestAttachMidTurnWaitsForTurnEnd(t *testing.T) {
	m := newTestModel(t)
	m.conv.Append(ollama.Message{Role: ollama.RoleUser, Content: "что в main.go?"})
	m.conv.Append(ollama.Message{Role: ollama.RoleAssistant, ToolCalls: []ollama.ToolCall{{
		ID: "call-1", Function: ollama.ToolCallFunc{Name: "read_file"}}}})
	stuckTurn(m)

	m.Update(attachMsg{rel: "notes.txt", body: "заметки", notice: "файл notes.txt приложен к контексту"})
	// Агент дописывает результат инструмента своей горутиной.
	m.conv.Append(ollama.Message{Role: ollama.RoleTool, ToolCallID: "call-1", Content: "package main"})
	m.Update(agentEventMsg{gen: m.gen.run, ev: agent.Event{Kind: agent.EventTurnDone}})

	msgs := m.conv.Messages()
	for i, msg := range msgs {
		if len(msg.ToolCalls) > 0 && (i+1 >= len(msgs) || msgs[i+1].Role != ollama.RoleTool) {
			t.Fatalf("за вызовом инструмента идёт не его результат: %+v", msgs)
		}
	}
	last := msgs[len(msgs)-1]
	if last.Role != ollama.RoleUser || !strings.Contains(last.Content, "заметки") {
		t.Fatalf("файл не приложен после хода: %+v", last)
	}
	if !strings.Contains(lastBlock(m).text, "notes.txt приложен") {
		t.Errorf("в ленте нет строки о вложении: %q", lastBlock(m).text)
	}
}

// Esc посреди хода тоже кладёт придержанный файл: ход кончился.
func TestAttachHeldUntilEsc(t *testing.T) {
	m := newTestModel(t)
	stuckTurn(m)
	m.Update(attachMsg{rel: "a.txt", body: "текст", notice: "файл a.txt приложен к контексту"})
	if m.conv.Len() != 0 {
		t.Fatal("файл лёг в историю посреди хода")
	}
	m.Update(keyPress("esc"))
	if m.conv.Len() != 1 {
		t.Fatalf("после остановки хода файл не приложен: сообщений %d", m.conv.Len())
	}
}

// Устаревший индекс живого блока не роняет программу: ответ продолжается
// новым блоком.
func TestStaleLiveIndexStartsNewBlock(t *testing.T) {
	m := newTestModel(t)
	stuckTurn(m)
	m.liveIdx, m.thinkIdx = len(m.blocks)+3, len(m.blocks)+5

	m.Update(agentEventMsg{gen: m.gen.run, ev: agent.Event{Kind: agent.EventThinking, Text: "думаю"}})
	streamText(m, "ответ")

	if got := lastBlock(m); got.kind != blockAssistant || got.text != "ответ" {
		t.Fatalf("ответ не продолжился новым блоком: %+v", got)
	}
}
