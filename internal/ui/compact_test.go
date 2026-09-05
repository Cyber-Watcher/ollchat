package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/ctxmeter"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

func TestNeedsCompaction(t *testing.T) {
	exact := ctxmeter.Meter{Capacity: 1000, Used: 800, Exact: true}
	if !needsCompaction(0.75, 6, exact, 10) {
		t.Error("80% при пороге 75% и длинной истории — пора")
	}
	if needsCompaction(0, 6, exact, 10) {
		t.Error("ноль выключает сжатие")
	}
	if needsCompaction(0.75, 6, ctxmeter.Meter{Capacity: 1000, Used: 800, Exact: false}, 10) {
		t.Error("по оценке, а не по точному числу, сжимать нельзя")
	}
	if needsCompaction(0.75, 6, ctxmeter.Meter{Capacity: 0, Used: 800, Exact: true}, 10) {
		t.Error("без известного окна сжимать нечего")
	}
	if needsCompaction(0.75, 6, exact, 6) {
		t.Error("история не длиннее хвоста — сжимать нечего")
	}
	if needsCompaction(0.75, 6, ctxmeter.Meter{Capacity: 1000, Used: 700, Exact: true}, 10) {
		t.Error("70% ниже порога 75%")
	}
}

func fillHistory(m *Model, n int) {
	for i := 0; i < n; i++ {
		role := ollama.RoleUser
		if i%2 == 1 {
			role = ollama.RoleAssistant
		}
		m.conv.Append(ollama.Message{Role: role, Content: strings.Repeat("т", 50)})
	}
}

// В агентном режиме при полном окне вопрос не уходит: подсказка про /compact,
// вопрос возвращён в поле, история не тронута.
func TestCompactRefusedInAgentMode(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Agent.CompactAt, m.cfg.Agent.CompactKeep = 0.75, 6
	m.modelCaps, m.modelRealTools = []string{"tools"}, true
	m.meter = ctxmeter.Meter{Capacity: 1000, Used: 900, Exact: true}
	fillHistory(m, 10)

	if cmd := m.send("ещё вопрос"); cmd != nil {
		t.Fatal("в агентном режиме отказ — без команды")
	}
	if got := lastBlock(m); got.kind != blockError || !strings.Contains(got.text, "/compact") {
		t.Fatalf("ожидалась подсказка про /compact: %+v", got)
	}
	if m.ta.Value() != "ещё вопрос" {
		t.Fatalf("вопрос должен вернуться в поле: %q", m.ta.Value())
	}
	if m.conv.Len() != 10 {
		t.Fatalf("история не должна меняться: %d", m.conv.Len())
	}
}

// В чате при полном окне сперва уходит команда сжатия, а не вопрос.
func TestCompactStartsBeforeQuestion(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Agent.CompactAt, m.cfg.Agent.CompactKeep = 0.75, 6
	m.modelCaps, m.modelRealTools = nil, false
	m.meter = ctxmeter.Meter{Capacity: 1000, Used: 900, Exact: true}
	fillHistory(m, 10)

	if cmd := m.send("ещё вопрос"); cmd == nil {
		t.Fatal("ожидалась команда сжатия")
	}
	if got := lastBlock(m); got.kind != blockHint || !strings.Contains(got.text, "сжимаю") {
		t.Fatalf("ожидалась строка о сжатии: %+v", got)
	}
	if m.conv.Len() != 10 {
		t.Fatalf("до ответа сжимателя история не меняется: %d", m.conv.Len())
	}
	if m.gen.compact != 1 {
		t.Fatalf("поколение сжатия: %d", m.gen.compact)
	}
}

// Сводка готова: старые сообщения заменены ею, оставлен хвост, заполнение
// стало оценочным, вопрос отправляется.
func TestCompactDoneAppliesSummary(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Agent.CompactAt, m.cfg.Agent.CompactKeep = 0.75, 6
	m.meter = ctxmeter.Meter{Capacity: 1000, Used: 900, Exact: true}
	fillHistory(m, 10)
	m.gen.compact = 1

	_, cmd := m.onCompactDone(compactDoneMsg{gen: 1, text: "вопрос", summary: "- всё важное",
		stats: ollama.Stats{PromptEvalCount: 500, EvalCount: 30}})
	if cmd == nil {
		t.Fatal("после сжатия вопрос должен уйти")
	}
	msgs := m.conv.Messages()
	// 4 старых → сводка, 6 хвоста, плюс сам вопрос, который send уже дописал.
	if len(msgs) < 7 || !strings.Contains(msgs[0].Content, "всё важное") {
		t.Fatalf("история после сжатия: %d сообщений, первое %q", len(msgs), msgs[0].Content)
	}
	if m.meter.Exact {
		t.Fatal("после сжатия заполнение — оценка, не точное число")
	}
	found := false
	for _, b := range m.blocks {
		if b.kind == blockHint && strings.Contains(b.text, "история сжата") {
			found = true
		}
	}
	if !found {
		t.Fatal("нет строки «история сжата»")
	}
	// Устаревшее поколение игнорируется.
	if _, cmd := m.onCompactDone(compactDoneMsg{gen: 0, text: "x"}); cmd != nil {
		t.Fatal("чужое поколение не должно ничего делать")
	}
}

// Сводка не удалась — история обрезана как /compact, вопрос всё равно уходит.
func TestCompactDoneFallsBackToTruncation(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Agent.CompactKeep = 6
	fillHistory(m, 10)
	m.gen.compact = 1
	_, cmd := m.onCompactDone(compactDoneMsg{gen: 1, text: "вопрос", err: errors.New("сервер занят")})
	if cmd == nil {
		t.Fatal("вопрос должен уйти и без сводки")
	}
	if got := m.conv.Messages(); len(got) < 6 || strings.Contains(got[0].Content, "Сводка") {
		t.Fatalf("ожидалась обрезка без сводки: %d сообщений", len(got))
	}
}
