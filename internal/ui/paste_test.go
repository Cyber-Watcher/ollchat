package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Вставка средствами терминала приходит отдельным сообщением. Если его не
// передать полю ввода, вставка молча пропадает — так и было после перехода
// на Bubble Tea v2.
func TestTerminalPasteReachesInput(t *testing.T) {
	m := newTestModel(t)
	m.ta.SetValue("до ")

	m.Update(tea.PasteMsg{Content: "вставленный текст"})

	if got := m.ta.Value(); got != "до вставленный текст" {
		t.Errorf("вставка не попала в поле ввода: %q", got)
	}
}

// Пока ждут ответа одной клавишей, вставлять некуда.
func TestTerminalPasteIgnoredWhilePickerOpen(t *testing.T) {
	m := newTestModel(t)
	m.openServerPicker()

	m.Update(tea.PasteMsg{Content: "мимо"})

	if strings.Contains(m.ta.Value(), "мимо") {
		t.Errorf("при открытом списке выбора вставка не должна попадать в промпт: %q", m.ta.Value())
	}
}
