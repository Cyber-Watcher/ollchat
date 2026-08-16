package ui

import tea "charm.land/bubbletea/v2"

// В Bubble Tea v2 нажатие клавиши описывается кодом и набором модификаторов,
// а не единственным полем Type, как было в v1. Помощники ниже избавляют тесты
// от повторения этой структуры и заодно показывают, как собрать нажатие руками.

// pressKey — нажатие клавиши без модификаторов: Esc, Enter, PgUp и подобные.
func pressKey(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

// pressCtrl — нажатие клавиши вместе с Ctrl.
func pressCtrl(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl}
}
