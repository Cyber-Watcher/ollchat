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

// pressMod — нажатие клавиши с произвольным набором модификаторов. Нужно для
// функциональных клавиш: Shift+F5 приходит и как shift+f5, и как отдельная
// клавиша f17 — в зависимости от того, какую последовательность шлёт терминал.
func pressMod(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}
