package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/agent"
	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/clipboard"
)

// catchClipboard подменяет запись в буфер обмена на время теста: настоящий
// буфер машины трогать нельзя, а проверить надо именно отданный текст.
func catchClipboard(t *testing.T, err error) *string {
	t.Helper()
	var got string
	prev := clipboardWrite
	clipboardWrite = func(_ context.Context, s string) error {
		got = s
		return err
	}
	t.Cleanup(func() { clipboardWrite = prev })
	return &got
}

// runCmd выполняет команду и скармливает её сообщение модели.
func runCmd(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("ожидалась команда копирования, получено nil")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("команда копирования не вернула сообщения")
	}
	m.Update(msg)
}

// clearTranscript убирает из ленты всё, что сложила туда сборка модели
// (например, подсказку о выключенных инструментах): тесты копирования считают
// блоки, и чужие записи в ленте сбивают им счёт.
func clearTranscript(m *Model) {
	m.blocks = nil
	m.rendered = nil
	m.refreshViewport(true)
}

// linesOf собирает многострочный текст заданной длины.
func linesOf(prefix string, n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "%s %d\n", prefix, i)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// TestBlockSpansMatchViewportContent сверяет вычисленные смещения блоков
// с настоящим содержимым ленты.
//
// Это защита от молчаливого расхождения с refreshViewport: если там изменится
// склейка блоков, копирование начнёт брать не тот ответ, и заметить это иначе
// можно только глазами на живом экране.
func TestBlockSpansMatchViewportContent(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	m.showThinking = false

	m.addBlock(block{kind: blockUser, text: "вопрос про горутины"})
	m.addBlock(block{kind: blockThinking, text: linesOf("рассуждение", 5)})
	m.addBlock(block{kind: blockAssistant, text: linesOf("ответ", 7)})
	m.addBlock(block{kind: blockTool, title: "read_file(main.go)", status: "ok", preview: "package main"})
	m.addBlock(block{kind: blockAssistant, text: linesOf("продолжение", 4)})

	spans := blockSpans(m.blocks, m.rendered)
	content := strings.Split(m.vp.GetContent(), "\n")

	if len(spans) != 4 {
		t.Fatalf("скрытые рассуждения не должны попадать в ленту, блоков в ленте: %d", len(spans))
	}
	total := 0
	for _, s := range spans {
		if s.start+s.lines > len(content) {
			t.Fatalf("блок %d выходит за пределы ленты: start=%d lines=%d, всего строк %d",
				s.idx, s.start, s.lines, len(content))
		}
		want := strings.Split(m.rendered[s.idx], "\n")
		got := content[s.start : s.start+s.lines]
		for i := range want {
			if want[i] != got[i] {
				t.Errorf("блок %d, строка %d: в ленте %q, в блоке %q", s.idx, i, got[i], want[i])
			}
		}
		total = s.start + s.lines
	}
	if total != m.vp.TotalLineCount() {
		t.Errorf("сумма строк блоков %d не сходится с лентой %d", total, m.vp.TotalLineCount())
	}
}

func TestVisibleAnswerPicksLargestShare(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	m.addBlock(block{kind: blockAssistant, text: linesOf("первый", 30)})
	m.addBlock(block{kind: blockAssistant, text: linesOf("второй", 30)})
	m.addBlock(block{kind: blockAssistant, text: linesOf("третий", 30)})

	spans := blockSpans(m.blocks, m.rendered)
	// Окно целиком внутри второго блока.
	second := spans[1]
	got := visibleAnswer(spans, second.start+2, 5)
	if got != 1 {
		t.Errorf("ожидался второй ответ (индекс 1), выбран %d", got)
	}
}

func TestVisibleAnswerTieGoesToLower(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	m.addBlock(block{kind: blockAssistant, text: linesOf("верхний", 10)})
	m.addBlock(block{kind: blockAssistant, text: linesOf("нижний", 10)})

	spans := blockSpans(m.blocks, m.rendered)
	// Окно из четырёх строк: две последние строки верхнего, разделитель
	// и две первые строки нижнего — доли равны.
	offset := spans[0].start + spans[0].lines - 2
	if got := visibleAnswer(spans, offset, 5); got != 1 {
		t.Errorf("при равной доле должен выигрывать нижний ответ, выбран %d", got)
	}
}

func TestVisibleAnswerNoneOnScreen(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	m.addBlock(block{kind: blockUser, text: linesOf("вопрос", 10)})
	m.addBlock(block{kind: blockTool, title: "grep(foo)", status: "ok", preview: linesOf("совпадение", 5)})

	spans := blockSpans(m.blocks, m.rendered)
	if got := visibleAnswer(spans, 0, 10); got != -1 {
		t.Errorf("ответов на экране нет, ожидалось -1, получено %d", got)
	}
}

func TestVisibleAnswerIgnoresHiddenThinking(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	m.showThinking = false
	m.addBlock(block{kind: blockThinking, text: linesOf("рассуждение", 40)})
	m.addBlock(block{kind: blockAssistant, text: linesOf("ответ", 5)})

	spans := blockSpans(m.blocks, m.rendered)
	if got := visibleAnswer(spans, 0, 10); got != 1 {
		t.Errorf("скрытые рассуждения не должны смещать выбор, выбран %d", got)
	}
	if spans[0].start != 0 {
		t.Errorf("первый видимый блок обязан начинаться со строки 0, получено %d", spans[0].start)
	}
}

func TestTurnAroundCollectsSplitAnswer(t *testing.T) {
	blocks := []block{
		{kind: blockUser, text: "первый вопрос"},
		{kind: blockAssistant, text: "старый ответ"},
		{kind: blockUser, text: "второй вопрос"},
		{kind: blockAssistant, text: "начало ответа"},
		{kind: blockThinking, text: "рассуждение"},
		{kind: blockTool, title: "read_file(a.go)", status: "ok", preview: "содержимое"},
		{kind: blockAssistant, text: "конец ответа"},
		{kind: blockUser, text: "третий вопрос"},
		{kind: blockAssistant, text: "следующий ответ"},
	}

	turn := turnAround(blocks, 6)
	if turn.user != 2 {
		t.Errorf("вопросом хода должен быть блок 2, получено %d", turn.user)
	}
	if len(turn.answers) != 2 || turn.answers[0] != 3 || turn.answers[1] != 6 {
		t.Errorf("оба куска ответа должны войти в ход, получено %v", turn.answers)
	}

	if got := answerText(blocks, turn); got != "начало ответа\n\nконец ответа" {
		t.Errorf("куски ответа склеены неверно: %q", got)
	}
}

func TestAnswerTextSkipsThinkingAndTools(t *testing.T) {
	blocks := []block{
		{kind: blockUser, text: "вопрос"},
		{kind: blockThinking, text: "тайные размышления"},
		{kind: blockTool, title: "bash(ls)", status: "ok", preview: "вывод инструмента"},
		{kind: blockAssistant, text: "ответ модели"},
	}
	got := answerText(blocks, turnAround(blocks, 3))
	if strings.Contains(got, "размышления") || strings.Contains(got, "вывод инструмента") {
		t.Errorf("в копию попало служебное: %q", got)
	}
	if got != "ответ модели" {
		t.Errorf("ожидался только текст ответа, получено %q", got)
	}
}

func TestCopyWithQuestionMatchesJournalFormat(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	asked := time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local)
	answered := time.Date(2026, 8, 16, 12, 5, 0, 0, time.Local)

	m.addBlock(block{kind: blockUser, text: "как работают горутины?", at: asked, turn: "k7f3-01"})
	m.addBlock(block{kind: blockAssistant, text: "Горутина — это лёгкий поток.",
		at: answered, model: "test-model", turn: "k7f3-01"})

	p, ok := m.copyVisibleAnswer(true)
	if !ok {
		t.Fatal("ответ на экране есть, копирование обязано его найти")
	}
	want := chatlog.FormatEntry("k7f3-01", asked, chatlog.KindQuestion, "", "как работают горутины?") +
		chatlog.FormatEntry("k7f3-01", answered, chatlog.KindAnswer, "test-model", "Горутина — это лёгкий поток.")
	want = strings.TrimRight(want, "\n") + "\n"
	if p.text != want {
		t.Errorf("формат разошёлся с журналом:\nполучено: %q\nожидалось: %q", p.text, want)
	}
}

func TestCopyAnswerWithoutQuestionHasNoHeaders(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	m.addBlock(block{kind: blockUser, text: "вопрос", at: time.Now()})
	m.addBlock(block{kind: blockAssistant, text: "ответ модели", at: time.Now(), model: "test-model"})

	p, ok := m.copyVisibleAnswer(false)
	if !ok {
		t.Fatal("ответ не найден")
	}
	if p.text != "ответ модели" {
		t.Errorf("F5 обязан давать голый текст ответа, получено %q", p.text)
	}
	if strings.Contains(p.text, "-----") {
		t.Error("в копии без вопроса не должно быть заголовков журнала")
	}
}

// TestCopyAnswerCopiesWholeAnswerBeyondScreen закрепляет решение пользователя:
// копируется весь ответ, даже если на экране видна лишь его часть.
func TestCopyAnswerCopiesWholeAnswerBeyondScreen(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	long := linesOf("строка ответа", 60)
	m.addBlock(block{kind: blockUser, text: "вопрос"})
	m.addBlock(block{kind: blockAssistant, text: long})

	// Встаём на середину ответа.
	m.vp.SetYOffset(20)

	p, ok := m.copyVisibleAnswer(false)
	if !ok {
		t.Fatal("ответ не найден")
	}
	if p.text != long {
		t.Errorf("ответ скопирован не целиком: %d символов вместо %d",
			len(p.text), len(long))
	}
}

func TestCopyKeyReportsNoAnswer(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	m.addBlock(block{kind: blockUser, text: "вопрос без ответа"})
	before := len(m.blocks)

	_, cmd := m.Update(pressKey(tea.KeyF5))
	if cmd != nil {
		t.Error("копировать нечего — команда не нужна")
	}
	if !strings.Contains(m.statusMsg, "нет ответа") {
		t.Errorf("пользователь должен узнать, что копировать нечего, в статусе: %q", m.statusMsg)
	}
	if len(m.blocks) != before {
		t.Error("сообщение об отсутствии ответа не должно добавлять блоков в ленту")
	}
}

func TestF5HandsTextToClipboard(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	got := catchClipboard(t, nil)

	m.addBlock(block{kind: blockUser, text: "вопрос", at: time.Now()})
	m.addBlock(block{kind: blockAssistant, text: "ответ модели", at: time.Now(), model: "test-model"})

	_, cmd := m.Update(pressKey(tea.KeyF5))
	runCmd(t, m, cmd)

	if *got != "ответ модели" {
		t.Errorf("в буфер отдан не тот текст: %q", *got)
	}
	if !strings.Contains(m.statusMsg, "скопировано") {
		t.Errorf("об успехе не сообщено, в статусе: %q", m.statusMsg)
	}
}

// TestShiftF5Variants закрепляет, что все три написания Shift+F5 работают
// одинаково: терминалы шлют его по-разному, а пользователь нажимает одно и то же.
func TestShiftF5Variants(t *testing.T) {
	keys := map[string]tea.KeyPressMsg{
		"shift+f5":  pressMod(tea.KeyF5, tea.ModShift),
		"f17":       pressKey(tea.KeyF17),
		"shift+f17": pressMod(tea.KeyF17, tea.ModShift),
	}

	var texts []string
	for name, key := range keys {
		m := newTestModel(t)
		clearTranscript(m)
		got := catchClipboard(t, nil)
		asked := time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local)
		m.addBlock(block{kind: blockUser, text: "вопрос", at: asked})
		m.addBlock(block{kind: blockAssistant, text: "ответ", at: asked, model: "test-model"})

		_, cmd := m.Update(key)
		if cmd == nil {
			t.Fatalf("клавиша %s не запустила копирование", name)
		}
		runCmd(t, m, cmd)
		if !strings.Contains(*got, chatlog.KindQuestion) {
			t.Errorf("клавиша %s должна копировать вместе с вопросом, получено %q", name, *got)
		}
		texts = append(texts, *got)
	}
	for i := 1; i < len(texts); i++ {
		if texts[i] != texts[0] {
			t.Errorf("разные написания Shift+F5 дали разный текст:\n%q\n%q", texts[0], texts[i])
		}
	}
}

func TestCopyFallsBackToOSC52(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	catchClipboard(t, fmt.Errorf("%w: сессии нет", clipboard.ErrNoWriter))

	m.addBlock(block{kind: blockUser, text: "вопрос"})
	m.addBlock(block{kind: blockAssistant, text: "ответ модели", model: "test-model"})

	_, cmd := m.Update(pressKey(tea.KeyF5))
	msg := cmd()
	copied, okMsg := msg.(answerCopiedMsg)
	if !okMsg {
		t.Fatalf("ожидалось answerCopiedMsg, получено %T", msg)
	}
	if !errors.Is(copied.err, clipboard.ErrNoWriter) {
		t.Fatalf("ошибка утилиты потерялась: %v", copied.err)
	}

	fallback := m.handleAnswerCopied(copied)
	if fallback == nil {
		t.Fatal("при отсутствии утилиты текст обязан уйти терминалу по OSC 52")
	}
	hints := 0
	for _, b := range m.blocks {
		if b.kind == blockHint {
			hints++
		}
	}
	if hints != 1 {
		t.Errorf("подсказка про OSC 52 должна появиться ровно один раз, показано: %d", hints)
	}

	// Повторное копирование подсказку не повторяет.
	m.handleAnswerCopied(copied)
	hints = 0
	for _, b := range m.blocks {
		if b.kind == blockHint {
			hints++
		}
	}
	if hints != 1 {
		t.Errorf("подсказка повторилась, всего: %d", hints)
	}
}

func TestCopyDuringStreamingTakesArrivedText(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	got := catchClipboard(t, nil)

	m.addBlock(block{kind: blockUser, text: "вопрос", at: time.Now()})
	m.streaming = true
	m.answeredBy = "test-model"
	m.handleAgentEvent(agent.Event{Kind: agent.EventContent, Text: "первая часть ответа"})

	_, cmd := m.Update(pressKey(tea.KeyF5))
	runCmd(t, m, cmd)

	if *got != "первая часть ответа" {
		t.Errorf("во время генерации копируется пришедшее, получено %q", *got)
	}
	if !strings.Contains(m.statusMsg, "не закончен") {
		t.Errorf("о незавершённости ответа надо предупредить, в статусе: %q", m.statusMsg)
	}
}

// TestCopyAfterDropLiveBlocks проверяет случай после неудачной попытки: блоки
// физически удаляются из ленты, и всё, что держит их индексы, обязано это пережить.
func TestCopyAfterDropLiveBlocks(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	got := catchClipboard(t, nil)

	m.addBlock(block{kind: blockUser, text: "вопрос", at: time.Now()})
	m.streaming = true
	m.answeredBy = "test-model"
	m.handleAgentEvent(agent.Event{Kind: agent.EventThinking, Text: "оборванное рассуждение"})
	m.handleAgentEvent(agent.Event{Kind: agent.EventRetry, Text: "повтор запроса"})
	m.handleAgentEvent(agent.Event{Kind: agent.EventContent, Text: "ответ со второй попытки"})
	m.handleAgentEvent(agent.Event{Kind: agent.EventTurnDone})

	_, cmd := m.Update(pressKey(tea.KeyF5))
	runCmd(t, m, cmd)

	if *got != "ответ со второй попытки" {
		t.Errorf("после повтора копируется удавшийся ответ, получено %q", *got)
	}
}

func TestCopyAfterClearReportsNoAnswer(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	m.addBlock(block{kind: blockUser, text: "вопрос"})
	m.addBlock(block{kind: blockAssistant, text: "ответ"})

	m.blocks = nil
	m.rendered = nil
	m.addBlock(block{kind: blockNotice, text: "история очищена"})

	_, cmd := m.Update(pressKey(tea.KeyF5))
	if cmd != nil {
		t.Error("после очистки копировать нечего")
	}
	if !strings.Contains(m.statusMsg, "нет ответа") {
		t.Errorf("ожидалось сообщение об отсутствии ответа, в статусе: %q", m.statusMsg)
	}
}

// TestStampTurnFillsTimeAndModel закрепляет, что у ответа есть чем заполнить
// заголовок журнала после завершения хода.
func TestStampTurnFillsTimeAndModel(t *testing.T) {
	m := newTestModel(t)
	clearTranscript(m)
	m.addBlock(block{kind: blockUser, text: "вопрос", at: time.Now()})
	m.streaming = true
	m.answeredBy = "test-model"

	m.handleAgentEvent(agent.Event{Kind: agent.EventContent, Text: "начало"})
	m.handleAgentEvent(agent.Event{Kind: agent.EventToolPlan,
		Tool: &agent.ToolEvent{Title: "read_file(a.go)"}})
	m.handleAgentEvent(agent.Event{Kind: agent.EventContent, Text: "конец"})
	m.handleAgentEvent(agent.Event{Kind: agent.EventTurnDone})

	answers := 0
	for _, b := range m.blocks {
		if b.kind != blockAssistant {
			continue
		}
		answers++
		if b.at.IsZero() {
			t.Error("у блока ответа нет отметки времени")
		}
		if b.model != "test-model" {
			t.Errorf("у блока ответа не проставлена модель: %q", b.model)
		}
	}
	if answers != 2 {
		t.Errorf("ход, разорванный инструментом, должен дать два блока ответа, получено %d", answers)
	}
}
