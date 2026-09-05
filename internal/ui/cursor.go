package ui

import (
	"image/color"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Курсор поля ввода рисует сам терминал: Bubble Tea сообщает ему координату,
// форму, мигание и цвет, а терминал накладывает курсор поверх ячейки. Поэтому
// символ под курсором остаётся виден и строка не сдвигается — ровно так, как
// ведёт себя обычная командная строка.
//
// Поддельный курсор, который bubbles рисует внутри текста, для этого выключается
// вызовом SetVirtualCursor(false): пока он включён, textarea.Cursor() возвращает
// nil и настоящего курсора на экране не будет.

// applyCursor задаёт вид курсора поля ввода по настройкам конфига.
func applyCursor(ta *textarea.Model, c config.Cursor) {
	ta.SetVirtualCursor(false)

	st := ta.Styles()
	st.Cursor.Shape = cursorShape(c.Shape)
	st.Cursor.Blink = c.Blink
	st.Cursor.Color = cursorColor(c.Color)
	ta.SetStyles(st)
}

// cursorShape переводит название формы из конфига в значение Bubble Tea.
// Значение уже проверено при загрузке конфига, поэтому неизвестное имя
// сюда не доходит и блок остаётся разумным запасным вариантом.
func cursorShape(name string) tea.CursorShape {
	switch name {
	case config.CursorUnderline:
		return tea.CursorUnderline
	case config.CursorBar:
		return tea.CursorBar
	default:
		return tea.CursorBlock
	}
}

// cursorColor разбирает запись цвета из конфига. Пустая строка означает
// «цвет терминала»: возвращается nil, и Bubble Tea цвет курсора не трогает.
func cursorColor(s string) color.Color {
	if s == "" {
		return nil
	}
	return lipgloss.Color(s)
}

// applyInputCursor — то же самое для однострочного поля ввода в модальных окнах.
//
// Типы стилей у textarea и textinput разные, поэтому общей функции не выходит,
// но правило одно: без SetVirtualCursor(false) поле вернёт nil вместо курсора,
// и в окне сохранения курсора на экране не будет вовсе.
func applyInputCursor(in *textinput.Model, c config.Cursor) {
	in.SetVirtualCursor(false)

	st := in.Styles()
	st.Cursor.Shape = cursorShape(c.Shape)
	st.Cursor.Blink = c.Blink
	st.Cursor.Color = cursorColor(c.Color)
	in.SetStyles(st)
}
