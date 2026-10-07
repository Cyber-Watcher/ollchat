package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/session"
)

// idleJob заводит задачу, которая ждёт, пока её не остановят: ход и итог
// тест приносит сам, сообщениями — так, как их приносит цикл событий.
func idleJob(t *testing.T, m *Model) int {
	t.Helper()
	m.startJob("индексация коллекции книги", func(ctx context.Context, _ func(kb.Progress)) error {
		<-ctx.Done()
		return nil
	})
	if m.job == nil {
		t.Fatal("задача не заведена")
	}
	job := m.job
	t.Cleanup(func() { job.cancel() })
	return m.gen.job
}

// Лента заменилась посреди индексации — /clear или /resume. Блок хода задачи
// запоминался индексом, и ход затирал то, что оказалось на этом месте:
// вопрос человека или ответ модели (аудит 07.10.2026).
func TestJobProgressSurvivesFeedReplacement(t *testing.T) {
	replace := map[string]func(m *Model){
		"/clear": func(m *Model) { m.runCommand("/clear") },
		"/resume": func(m *Model) {
			m.Update(sessionLoadedMsg{rec: &session.Saved{
				SavedAt: time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC), Server: "s", Model: "m",
				Messages: []ollama.Message{{Role: ollama.RoleUser, Content: "старый вопрос"},
					{Role: ollama.RoleAssistant, Content: "старый ответ"}}}})
		},
	}
	for name, do := range replace {
		t.Run(name, func(t *testing.T) {
			m := newTestModel(t)
			m.addBlock(block{kind: blockNotice, text: "что-то до задачи"})
			at := len(m.blocks) // здесь встанет блок хода
			gen := idleJob(t, m)

			do(m)
			for len(m.blocks) <= at+1 {
				m.addBlock(block{kind: blockUser, text: "вопрос человека"})
			}
			before := m.blocks[at]

			m.Update(jobProgressMsg{gen: gen, p: kb.Progress{Phase: "чтение", DocsDone: 1, DocsTotal: 3}})
			if m.blocks[at].text != before.text || m.blocks[at].kind != before.kind {
				t.Fatalf("ход задачи затёр чужой блок: было %q, стало %q", before.text, m.blocks[at].text)
			}
			if got := lastBlock(m).text; !strings.Contains(got, "чтение 1/3") {
				t.Errorf("ход задачи не виден в ленте: %q", got)
			}

			m.Update(jobDoneMsg{gen: gen, p: kb.Progress{Done: true, Added: 3, DocsDone: 3}})
			if m.blocks[at].text != before.text {
				t.Fatalf("итог задачи затёр чужой блок: %q", m.blocks[at].text)
			}
			if got := lastBlock(m).text; !strings.Contains(got, "добавлено книг: 3") {
				t.Errorf("итог задачи не виден в ленте: %q", got)
			}
			if m.job != nil {
				t.Error("задача не закрыта")
			}
		})
	}
}

// Без замены ленты строка хода живёт на своём месте и не размножается.
func TestJobProgressUpdatesInPlace(t *testing.T) {
	m := newTestModel(t)
	gen := idleJob(t, m)
	at := len(m.blocks) - 1
	m.addBlock(block{kind: blockUser, text: "вопрос посреди индексации"})
	n := len(m.blocks)

	for i := 1; i <= 3; i++ {
		m.Update(jobProgressMsg{gen: gen, p: kb.Progress{Phase: "чтение", DocsDone: i, DocsTotal: 3}})
	}
	if len(m.blocks) != n {
		t.Fatalf("лента выросла от хода задачи: %d → %d блоков", n, len(m.blocks))
	}
	if !strings.Contains(m.blocks[at].text, "чтение 3/3") {
		t.Errorf("ход задачи не на своём месте: %q", m.blocks[at].text)
	}
	if m.blocks[at+1].text != "вопрос посреди индексации" {
		t.Errorf("вопрос человека затёрт: %q", m.blocks[at+1].text)
	}
}
