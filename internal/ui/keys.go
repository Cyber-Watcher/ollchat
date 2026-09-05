package ui

import "charm.land/bubbles/v2/key"

// keymap — клавиши приложения одним списком (этап 91, R6.10).
//
// До того handleKey сравнивал нажатие со строковыми литералами, а справка
// перечисляла клавиши отдельным текстом; тест keysdrift сверяет их.
// Тройные написания (`shift+f5`, `shift+f17`, `f17`) — потому что часть
// терминалов шлёт старые последовательности, которые разборщик отдаёт
// отдельными клавишами без модификатора.
type keymap struct {
	quit        key.Binding
	esc         key.Binding
	enter       key.Binding
	up          key.Binding
	down        key.Binding
	mode        key.Binding
	think       key.Binding
	mouse       key.Binding
	images      key.Binding
	find        key.Binding
	savePDF     key.Binding
	savePDFFull key.Binding
	copy        key.Binding
	copyFull    key.Binding
	paste       key.Binding
	servers     key.Binding
	models      key.Binding
	pageUp      key.Binding
	pageDown    key.Binding
	halfUp      key.Binding
	halfDown    key.Binding
	top         key.Binding
	bottom      key.Binding
}

var keys = keymap{
	quit:        key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("Ctrl+C", "выход (дважды)")),
	esc:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "прервать ответ")),
	enter:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "отправить")),
	up:          key.NewBinding(key.WithKeys("up")),
	down:        key.NewBinding(key.WithKeys("down")),
	mode:        key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("Shift+Tab", "сменить режим")),
	think:       key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("Ctrl+T", "скрыть/показать рассуждения")),
	mouse:       key.NewBinding(key.WithKeys("f2"), key.WithHelp("F2", "мышь приложению или терминалу")),
	images:      key.NewBinding(key.WithKeys("f3"), key.WithHelp("F3", "панель вложенных изображений")),
	find:        key.NewBinding(key.WithKeys("ctrl+f"), key.WithHelp("Ctrl+F", "список найденного")),
	savePDF:     key.NewBinding(key.WithKeys("f4"), key.WithHelp("F4", "сохранить видимый ответ в PDF")),
	savePDFFull: key.NewBinding(key.WithKeys("shift+f4", "shift+f16", "f16"), key.WithHelp("Shift+F4", "то же вместе с вопросом")),
	copy:        key.NewBinding(key.WithKeys("f5"), key.WithHelp("F5", "копировать видимый ответ")),
	copyFull:    key.NewBinding(key.WithKeys("shift+f5", "shift+f17", "f17"), key.WithHelp("Shift+F5", "то же вместе с вопросом")),
	paste:       key.NewBinding(key.WithKeys("ctrl+v"), key.WithHelp("Ctrl+V", "вставить изображение из буфера обмена")),
	servers:     key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("Ctrl+S", "выбор сервера")),
	models:      key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("Ctrl+R", "выбор модели")),
	pageUp:      key.NewBinding(key.WithKeys("pgup"), key.WithHelp("PgUp", "страница вверх")),
	pageDown:    key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("PgDn", "страница вниз")),
	halfUp:      key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("Ctrl+U", "полстраницы вверх")),
	halfDown:    key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("Ctrl+D", "полстраницы вниз")),
	top:         key.NewBinding(key.WithKeys("ctrl+home"), key.WithHelp("Ctrl+Home", "в начало")),
	bottom:      key.NewBinding(key.WithKeys("ctrl+g", "ctrl+end"), key.WithHelp("Ctrl+G", "в конец ответа")),
}

// all — все привязки, ради проверок и справки.
func (k keymap) all() []key.Binding {
	return []key.Binding{k.quit, k.esc, k.enter, k.up, k.down, k.mode, k.think, k.mouse, k.images,
		k.find, k.savePDF, k.savePDFFull, k.copy, k.copyFull, k.paste, k.servers, k.models,
		k.pageUp, k.pageDown, k.halfUp, k.halfDown, k.top, k.bottom}
}
