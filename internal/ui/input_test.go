package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Поле ввода высотой в три строки: длинный промпт должен прокручиваться
// внутри него, а курсор — оставаться видимым.
//
// Дефект, который это закрывает: перенос строки вставлялся напрямую,
// в обход Update, поэтому внутренний просмотр поля не двигался за курсором.
// Курсор оказывался ниже видимой области, и со стороны это выглядело так,
// будто Alt+Enter не работает вовсе.

// altEnter — перенос строки в промпте.
func altEnter() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
}

// cursorInsideInput проверяет, что курсор попал в видимые строки поля.
func cursorInsideInput(t *testing.T, m *Model, tag string) {
	t.Helper()
	c := m.ta.Cursor()
	if c == nil {
		t.Fatalf("%s: поле ввода не отдаёт курсор", tag)
	}
	if c.Y < 0 || c.Y >= m.ta.Height() {
		t.Errorf("%s: курсор на строке %d, а видимых строк %d — он вне поля",
			tag, c.Y, m.ta.Height())
	}
}

func TestAltEnterAddsLinesAndKeepsCursorVisible(t *testing.T) {
	m := newTestModel(t)

	for i := 1; i <= 6; i++ {
		typeText(m, "строка")
		m.Update(altEnter())
		cursorInsideInput(t, m, "после переноса №"+string(rune('0'+i)))
	}

	if got := strings.Count(m.ta.Value(), "\n"); got != 6 {
		t.Errorf("переносов в промпте %d, ожидалось 6: %q", got, m.ta.Value())
	}
	// Поле показывает хвост промпта, а не начало.
	if view := m.ta.View(); strings.Count(view, "\n") != m.ta.Height()-1 {
		t.Errorf("поле ввода должно оставаться высотой %d строк: %q", m.ta.Height(), view)
	}
}

// Ctrl+J — вторая клавиша переноса, она тоже должна работать.
func TestCtrlJAddsLine(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "первая")
	m.Update(pressCtrl('j'))
	typeText(m, "вторая")

	if got := m.ta.Value(); got != "первая\nвторая" {
		t.Errorf("значение промпта %q, ожидалось \"первая\\nвторая\"", got)
	}
}

// Стрелки ходят по промпту, прокручивая поле, и курсор не теряется.
func TestArrowsNavigateLongPrompt(t *testing.T) {
	m := newTestModel(t)
	for i := 1; i <= 6; i++ {
		typeText(m, "строка")
		m.Update(altEnter())
	}

	for i := 0; i < 5; i++ {
		m.Update(pressKey(tea.KeyUp))
		cursorInsideInput(t, m, "при движении вверх")
	}
	if m.ta.Line() == 6 {
		t.Error("стрелка вверх должна двигать курсор по строкам промпта")
	}

	for i := 0; i < 5; i++ {
		m.Update(pressKey(tea.KeyDown))
		cursorInsideInput(t, m, "при движении вниз")
	}
}

// Влево-вправо ходят по символам и не трогают ленту.
func TestHorizontalArrowsMoveWithinPrompt(t *testing.T) {
	m := newTestModel(t)
	fillTranscript(m, 200)
	typeText(m, "привет")

	before := m.vp.YOffset()
	for i := 0; i < 3; i++ {
		m.Update(pressKey(tea.KeyLeft))
	}
	typeText(m, "-")

	if got := m.ta.Value(); got != "при-вет" {
		t.Errorf("после трёх ← и ввода получилось %q, ожидалось \"при-вет\"", got)
	}
	if m.vp.YOffset() != before {
		t.Error("стрелки в промпте не должны прокручивать ленту")
	}
}

// Enter по-прежнему отправляет, а не переносит строку.
func TestEnterStillSends(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "вопрос")
	m.Update(pressKey(tea.KeyEnter))

	if m.conv.Len() != 1 {
		t.Fatalf("вопрос не отправлен, сообщений в истории: %d", m.conv.Len())
	}
	if m.ta.Value() != "" {
		t.Errorf("после отправки поле должно очищаться, там %q", m.ta.Value())
	}
}
