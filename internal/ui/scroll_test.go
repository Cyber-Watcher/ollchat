package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// newTestModel собирает модель интерфейса с отключённым журналом.
func newTestModel(t *testing.T) *Model {
	t.Helper()
	return newTestModelWith(t, nil)
}

// newTestModelWith собирает модель, дав тесту поправить конфиг перед сборкой.
func newTestModelWith(t *testing.T, tune func(*config.Config)) *Model {
	t.Helper()
	cfg := config.Default()
	cfg.General.RenderMarkdown = false // markdown не нужен, важна геометрия
	if tune != nil {
		tune(cfg)
	}

	sb, err := permissions.NewSandbox(t.TempDir(), false, false, 512)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	set, err := permissions.Compile(cfg.Permissions.Allow, cfg.Permissions.Ask, cfg.Permissions.Deny, sb.Root())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	reg, err := tools.NewRegistry(cfg.Agent.Tools, tools.Options{Sandbox: sb, MaxOutputKB: 64})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	m := New(cfg,
		permissions.NewGuard(set, sb, permissions.ModeSafe),
		reg,
		chatlog.New(t.TempDir(), "chat.md", false),
		session.NewStore(t.TempDir()),
		&cfg.Servers[0],
		"test-model")

	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

// fillTranscript добавляет в ленту достаточно строк, чтобы появилась прокрутка.
func fillTranscript(m *Model, lines int) {
	var sb strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&sb, "строка ответа номер %d\n", i)
	}
	m.addBlock(block{kind: blockAssistant, text: sb.String()})
}

func TestScrollKeysMoveViewport(t *testing.T) {
	m := newTestModel(t)
	fillTranscript(m, 200)

	if !m.vp.AtBottom() {
		t.Fatal("после добавления текста лента должна стоять внизу")
	}

	m.Update(pressKey(tea.KeyPgUp))
	afterPgUp := m.vp.YOffset()
	if m.vp.AtBottom() {
		t.Error("после PgUp лента не должна оставаться внизу")
	}

	m.Update(pressCtrl('u'))
	if m.vp.YOffset() >= afterPgUp {
		t.Errorf("Ctrl+U должен прокручивать выше: было %d, стало %d", afterPgUp, m.vp.YOffset())
	}

	m.Update(pressKey(tea.KeyPgDown))
	if m.vp.YOffset() <= 0 && afterPgUp > 0 {
		t.Error("PgDn должен прокручивать вниз")
	}
}

func TestMouseWheelScrolls(t *testing.T) {
	m := newTestModel(t)
	fillTranscript(m, 200)

	before := m.vp.YOffset()
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.vp.YOffset() >= before {
		t.Fatalf("колесо мыши вверх должно прокручивать ленту: было %d, стало %d", before, m.vp.YOffset())
	}

	up := m.vp.YOffset()
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.vp.YOffset() <= up {
		t.Errorf("колесо мыши вниз должно возвращать ленту обратно: было %d, стало %d", up, m.vp.YOffset())
	}
}

func TestCtrlGJumpsToEnd(t *testing.T) {
	m := newTestModel(t)
	fillTranscript(m, 200)

	m.Update(pressKey(tea.KeyPgUp))
	m.Update(pressKey(tea.KeyPgUp))
	if m.vp.AtBottom() {
		t.Fatal("подготовка: лента должна быть отлистана вверх")
	}

	m.Update(pressCtrl('g'))
	if !m.vp.AtBottom() {
		t.Error("Ctrl+G должен возвращать в конец ленты")
	}
}

func TestScrollHintShowsRemainingLines(t *testing.T) {
	m := newTestModel(t)
	fillTranscript(m, 200)

	if strings.Contains(m.separatorView(), "ещё") {
		t.Error("внизу ленты подсказка о непрочитанных строках не нужна")
	}

	m.Update(pressKey(tea.KeyPgUp))
	sep := m.separatorView()
	if !strings.Contains(sep, "ещё") || !strings.Contains(sep, "Ctrl+G") {
		t.Fatalf("после прокрутки вверх ожидалась подсказка со счётчиком, получено: %q", sep)
	}

	below := m.linesBelow()
	if below <= 0 {
		t.Fatalf("linesBelow = %d, ожидалось положительное число", below)
	}
	if !strings.Contains(sep, fmt.Sprintf("%d", below)) {
		t.Errorf("в подсказке нет числа оставшихся строк (%d): %q", below, sep)
	}

	// Подсказка встроена в разделитель и не должна менять его ширину.
	if w := visibleWidth(sep); w != m.width {
		t.Errorf("ширина разделителя = %d, ожидалась %d", w, m.width)
	}

	m.Update(pressCtrl('g'))
	if strings.Contains(m.separatorView(), "ещё") {
		t.Error("после возврата в конец подсказка должна исчезать")
	}
}

func TestStreamingDoesNotStealScrollPosition(t *testing.T) {
	m := newTestModel(t)
	fillTranscript(m, 200)

	m.streaming = true
	m.Update(pressKey(tea.KeyPgUp))
	m.Update(pressKey(tea.KeyPgUp))
	parked := m.vp.YOffset()

	// Во время генерации приходит новый текст — позиция чтения не должна съезжать.
	m.liveIdx = m.addBlock(block{kind: blockAssistant, text: "новая строка"})
	for i := 0; i < 20; i++ {
		b := m.blocks[m.liveIdx]
		b.text += fmt.Sprintf("\nещё кусок ответа %d", i)
		m.blocks[m.liveIdx] = b
		m.rendered[m.liveIdx] = wrap(b.text, m.rend.width)
		m.refreshViewport(true)
	}

	if m.vp.YOffset() != parked {
		t.Errorf("во время генерации позиция прокрутки съехала: было %d, стало %d", parked, m.vp.YOffset())
	}
	if !strings.Contains(m.separatorView(), "автопрокрутка на паузе") {
		t.Errorf("во время генерации должна сообщаться пауза автопрокрутки: %q", m.separatorView())
	}

	// Возврат в конец снова включает слежение за ответом.
	m.Update(pressCtrl('g'))
	if !m.vp.AtBottom() {
		t.Fatal("Ctrl+G должен вернуть в конец и во время генерации")
	}
	b := m.blocks[m.liveIdx]
	b.text += "\nхвост ответа"
	m.blocks[m.liveIdx] = b
	m.rendered[m.liveIdx] = wrap(b.text, m.rend.width)
	m.refreshViewport(true)
	if !m.vp.AtBottom() {
		t.Error("после возврата в конец лента должна снова следовать за ответом")
	}
}

func TestPluralForms(t *testing.T) {
	cases := map[int]string{
		1: "строка", 2: "строки", 4: "строки", 5: "строк",
		11: "строк", 12: "строк", 14: "строк",
		21: "строка", 22: "строки", 25: "строк", 101: "строка", 111: "строк",
	}
	for n, want := range cases {
		if got := plural(n, "строка", "строки", "строк"); got != want {
			t.Errorf("plural(%d) = %q, ожидалось %q", n, got, want)
		}
	}
}

// visibleWidth считает ширину строки без управляющих последовательностей.
func visibleWidth(s string) int {
	var out []rune
	inEsc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEsc = true
		case inEsc && (r == 'm' || r == 'K' || r == 'H'):
			inEsc = false
		case !inEsc:
			out = append(out, r)
		}
	}
	return len(out)
}
