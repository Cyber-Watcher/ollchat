package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/ctxmeter"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/session"
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

// agentTurns — история агентного режима: ходов n, в каждом вызов bash и его
// вывод размером out байт.
func agentTurns(m *Model, n, out int) {
	for i := 0; i < n; i++ {
		m.conv.Append(ollama.Message{Role: ollama.RoleUser, Content: fmt.Sprintf("вопрос %d", i)})
		m.conv.Append(ollama.Message{Role: ollama.RoleAssistant, ToolCalls: []ollama.ToolCall{{ID: fmt.Sprint("c", i),
			Function: ollama.ToolCallFunc{Name: "bash", Arguments: map[string]any{"command": "ls"}}}}})
		m.conv.Append(ollama.Message{Role: ollama.RoleTool, ToolName: "bash", Content: strings.Repeat("x", out)})
		m.conv.Append(ollama.Message{Role: ollama.RoleAssistant, Content: "готово"})
	}
}

// В агентном режиме при полном окне сперва без модели убираются старые выводы
// инструментов, и хватило — вопрос уходит сразу. До этапа 133 здесь был отказ:
// вопрос возвращался в поле с подсказкой «/compact или /context add 32k», и на
// рабочей машине человек стирал вопрос, чтобы набрать /compact (решение
// 04.09.2026 «в агентном режиме не сжимать» отменено владельцем).
func TestAgentCompactionPrunesToolOutputsFirst(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Agent.CompactAt, m.cfg.Agent.CompactKeep, m.cfg.Agent.PruneKeepTurns = 0.75, 6, 2
	m.modelCaps, m.modelRealTools = []string{"tools"}, true
	m.meter = ctxmeter.Meter{Capacity: 10000, Used: 9000, Exact: true}
	agentTurns(m, 3, 30000)
	t.Cleanup(m.stopStreaming)

	if cmd := m.send("ещё вопрос"); cmd == nil || m.compacting {
		t.Fatalf("вопрос должен уйти сразу, без сводки (сжатие идёт: %v)", m.compacting)
	}
	msgs := m.conv.Messages()
	if msgs[2].Content != session.PrunedToolOutput {
		t.Fatalf("вывод первого хода не убран: %.40q", msgs[2].Content)
	}
	if len(msgs[2+4].Content) != 30000 || len(msgs[2+8].Content) != 30000 {
		t.Fatal("выводы двух последних ходов должны остаться целиком")
	}
	if len(msgs[1].ToolCalls) != 1 {
		t.Fatal("вызов с аргументами должен остаться")
	}
	found := false
	for _, b := range m.blocks {
		found = found || b.kind == blockCompact && strings.Contains(b.text, "убраны выводы 1 вызовов") &&
			strings.Contains(b.text, "→ ≈")
	}
	if !found {
		t.Fatal("человеку не сказано, что выводы убраны и сколько окна стало")
	}
	// Обрезка мгновенна, но индикатор её показывает: отсчёт от 9000 к новому.
	if m.ctxAnim.left != ctxDropFrames || m.ctxAnim.from != 9000 {
		t.Fatalf("отсчёт индикатора не начат: осталось кадров %d, от %d", m.ctxAnim.left, m.ctxAnim.from)
	}
}

// Убрать нечего (выводов инструментов нет) — в агентном режиме, как в чате,
// сводка, а вопрос ждёт её и уйдёт следом; в поле он не возвращается.
func TestAgentCompactionFallsBackToSummary(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Agent.CompactAt, m.cfg.Agent.CompactKeep, m.cfg.Agent.PruneKeepTurns = 0.75, 6, 2
	m.modelCaps, m.modelRealTools = []string{"tools"}, true
	m.meter = ctxmeter.Meter{Capacity: 1000, Used: 900, Exact: true}
	fillHistory(m, 10)

	if cmd := m.send("ещё вопрос"); cmd == nil || !m.compacting || m.compactText != "ещё вопрос" {
		t.Fatalf("ждали сжатие сводкой с отложенным вопросом: сжатие %v, вопрос %q", m.compacting, m.compactText)
	}
	if m.ta.Value() != "" {
		t.Fatalf("вопрос не должен возвращаться в поле: %q", m.ta.Value())
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
	if got := lastBlock(m); got.kind != blockCompact || !strings.Contains(got.text, "сжимаю") {
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
		if b.kind == blockCompact && strings.Contains(b.text, "история сжата") && strings.Contains(b.text, "900 → ≈") {
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

// compactingModel — чат без инструментов с полным окном: Enter запускает
// сжатие истории, и вопрос ждёт его конца.
func compactingModel(t *testing.T) *Model {
	t.Helper()
	m := newTestModel(t)
	m.cfg.Agent.CompactAt, m.cfg.Agent.CompactKeep = 0.75, 6
	m.modelCaps, m.modelRealTools = nil, false
	m.meter = ctxmeter.Meter{Capacity: 1000, Used: 900, Exact: true}
	fillHistory(m, 10)
	// Вопрос после сжатия уходит в ход; в конце теста ход закрывается.
	t.Cleanup(m.stopStreaming)
	return m
}

// ask набирает вопрос и жмёт Enter, как человек.
func ask(m *Model, text string) tea.Cmd {
	m.ta.SetValue(text)
	_, cmd := m.Update(pressKey(tea.KeyEnter))
	return cmd
}

// Второй Enter во время сжатия запускал второе Summarize, ответ на первое
// отбрасывался по поколению — и первый вопрос пропадал (аудит 07.10.2026).
func TestCompactionTakesNoSecondQuestion(t *testing.T) {
	m := compactingModel(t)
	if cmd := ask(m, "первый вопрос"); cmd == nil || !m.compacting {
		t.Fatal("подготовка: сжатие не началось")
	}
	gen := m.gen.compact

	ask(m, "второй вопрос")
	if m.gen.compact != gen {
		t.Fatal("второй Enter запустил второе сжатие")
	}
	if m.ta.Value() != "второй вопрос" {
		t.Errorf("набранное не должно пропадать из поля: %q", m.ta.Value())
	}
	if !strings.Contains(m.statusMsg, "сжимаю") {
		t.Errorf("человеку не сказано, почему вопрос не ушёл: %q", m.statusMsg)
	}

	m.Update(compactDoneMsg{gen: gen, text: "первый вопрос", summary: "- сводка"})
	if m.compacting {
		t.Error("сжатие не закрыто")
	}
	msgs := m.conv.Messages()
	if last := msgs[len(msgs)-1]; last.Content != "первый вопрос" {
		t.Fatalf("после сжатия ушёл не первый вопрос: %q", last.Content)
	}
}

// Пока модель пишет сводку, индикатор контекста — на оранжевой подложке, по
// полосе бежит отрезок, рядом сколько сводки написано; когда сводка готова,
// число опускается от прежнего к новому, по дороге не растёт, и кадры
// кончаются (этап 134, слово владельца: «индикатор контекста использовать как
// прогресс бар… показатель заполненности должен уменьшаться»).
func TestCompactionIndicatorRunsThenCountsDown(t *testing.T) {
	m := compactingModel(t)
	if cmd := ask(m, "вопрос"); cmd == nil || !m.compacting {
		t.Fatal("подготовка: сжатие не началось")
	}
	now := time.Now()
	before := m.ctxIndicator(now)
	if !strings.Contains(before, "сжатие") || !strings.Contains(before, "900/1.0k") {
		t.Fatalf("индикатор во время сводки: %q", before)
	}
	m.Update(ctxAnimMsg{})
	if after := m.ctxIndicator(now); after == before {
		t.Error("отрезок по полосе не сдвинулся")
	}
	m.ctxAnim.written.Store(3000)
	if want := "сводка ≈" + ctxmeter.FormatTokens(ctxmeter.EstimateChars(3000)); !strings.Contains(m.ctxIndicator(now), want) {
		t.Errorf("не видно, сколько сводки написано (%q): %q", want, m.ctxIndicator(now))
	}

	m.Update(compactDoneMsg{gen: m.gen.compact, text: "вопрос", summary: "- сводка"})
	if m.compacting || m.ctxAnim.left != ctxDropFrames {
		t.Fatalf("после сводки: сжатие %v, кадров отсчёта %d", m.compacting, m.ctxAnim.left)
	}
	prev := m.ctxShown()
	if prev != 900 || m.meter.Used >= 900 {
		t.Fatalf("отсчёт должен идти от 900 вниз: показано %d, стало %d", prev, m.meter.Used)
	}
	var last tea.Cmd
	for i := range ctxDropFrames {
		_, last = m.Update(ctxAnimMsg{})
		cur := m.ctxShown()
		if cur > prev {
			t.Fatalf("кадр %d: число выросло %d → %d", i, prev, cur)
		}
		prev = cur
	}
	if prev != m.meter.Used || last != nil {
		t.Fatalf("отсчёт не дошёл до нынешнего числа или кадры не кончились: %d из %d, кадр назначен %v",
			prev, m.meter.Used, last != nil)
	}
	if got, want := m.ctxIndicator(now), ctxStyle(m.meter.Percent()).Render(m.meter.String(ctxBarWidth)); got != want {
		t.Errorf("после отсчёта индикатор не вернулся к обычному виду: %q", got)
	}
}

// /compact — тоже сжатие: индикатор опускается от прежнего числа к новому.
func TestManualCompactCountsDown(t *testing.T) {
	m := newTestModel(t)
	m.meter = ctxmeter.Meter{Capacity: 1000, Used: 900, Exact: true}
	fillHistory(m, 10)
	if cmd := m.compactCmd("2"); cmd == nil || m.ctxAnim.left != ctxDropFrames || m.ctxShown() != 900 {
		t.Fatalf("отсчёт после /compact: команда %v, кадров %d, показано %d", cmd != nil, m.ctxAnim.left, m.ctxShown())
	}
}

// Строка о сжатии в ленте — свой вид: бледная подсказка уезжала вверх под
// выводом инструментов незамеченной.
func TestCompactLineRendersApart(t *testing.T) {
	r := newRenderer(80, false, config.Theme{Style: "dark"})
	text := "история сжата: окно 900 → ≈300"
	got := r.Render(block{kind: blockCompact, text: text}, false)
	if !strings.Contains(got, "⇣") || !strings.Contains(got, "900 → ≈300") {
		t.Fatalf("строка о сжатии: %q", got)
	}
	if got == r.Render(block{kind: blockHint, text: text}, false) {
		t.Fatal("строка о сжатии выглядит как подсказка")
	}
}

// Esc прерывает сжатие — и только его: индексация, идущая рядом, живёт дальше.
// Вопрос ещё не ушёл никуда и возвращается в поле ввода.
func TestEscCancelsCompaction(t *testing.T) {
	m := compactingModel(t)
	idleJob(t, m)
	ask(m, "вопрос")
	gen := m.gen.compact

	m.Update(keyPress("esc"))
	if m.compacting {
		t.Fatal("Esc не прервал сжатие")
	}
	if m.job == nil {
		t.Fatal("Esc остановил индексацию вместо сжатия")
	}
	if m.ta.Value() != "вопрос" {
		t.Errorf("вопрос не вернулся в поле ввода: %q", m.ta.Value())
	}
	n := m.conv.Len()
	m.Update(compactDoneMsg{gen: gen, text: "вопрос", summary: "- сводка"})
	if m.conv.Len() != n || m.streaming {
		t.Fatal("ответ прерванного сжатия применён")
	}
}

// /clear и смена сервера бросают сжатие: сводка прежней истории не ложится
// в очищенную, а вопрос не уходит туда, куда его не задавали.
func TestCompactionDroppedOnClearAndServerSwitch(t *testing.T) {
	cases := map[string]func(m *Model){
		"/clear":        func(m *Model) { m.clearCmd("") },
		"смена сервера": func(m *Model) { m.switchServer(m.cfg.Servers[0].Name) },
	}
	for name, do := range cases {
		t.Run(name, func(t *testing.T) {
			m := compactingModel(t)
			ask(m, "вопрос")
			gen := m.gen.compact

			do(m)
			if m.compacting {
				t.Fatal("сжатие не брошено")
			}
			n := m.conv.Len()
			m.Update(compactDoneMsg{gen: gen, text: "вопрос", summary: "- сводка прежней истории"})
			if m.conv.Len() != n || m.streaming {
				t.Fatalf("ответ брошенного сжатия применён: %d → %d сообщений", n, m.conv.Len())
			}
			if m.ta.Value() != "вопрос" {
				t.Errorf("вопрос не вернулся человеку: %q", m.ta.Value())
			}
		})
	}
}

// Файл /add, дочитанный во время сжатия, ложится в историю после сводки
// и до вопроса: иначе CompactWith вытеснил бы им из хвоста сообщение,
// которого нет и в сводке.
func TestAttachWaitsForCompaction(t *testing.T) {
	m := compactingModel(t)
	ask(m, "что в файле?")
	gen := m.gen.compact

	m.Update(attachMsg{rel: "a.txt", body: "содержимое", notice: "файл a.txt приложен к контексту"})
	if m.conv.Len() != 10 {
		t.Fatalf("файл лёг в историю посреди сжатия: %d сообщений", m.conv.Len())
	}
	m.Update(compactDoneMsg{gen: gen, text: "что в файле?", summary: "- сводка"})
	msgs := m.conv.Messages()
	if len(msgs) < 2 || !strings.Contains(msgs[len(msgs)-2].Content, "содержимое") ||
		msgs[len(msgs)-1].Content != "что в файле?" {
		t.Fatalf("файл и вопрос не на своих местах: %+v", msgs)
	}
}
