package ui

import (
	"os"
	"reflect"
	"strings"

	"charm.land/glamour/v2/ansi"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Курсив в терминале: почему без него нельзя обойтись просто так.
//
// Заметки и рассуждения ollchat рисует курсивом (SGR 3). Но терминал может
// курсив не уметь, и тогда он показывает вместо него что-нибудь другое —
// чаще всего инверсию, то есть серую заливку строки. Именно так выглядит
// справка внутри tmux: у него по умолчанию TERM=screen-256color, а в терминфо
// screen нет возможности sitm (курсив). Проверено на стенде: у screen
// и screen-256color sitm нет, у tmux-256color и xterm-256color есть.
//
// Настоящее лекарство у пользователя одно — прописать в ~/.tmux.conf
//
//	set -g default-terminal "tmux-256color"
//
// но приложение не должно выглядеть сломанным до того, как кто-то это сделает.
// Поэтому курсив выключается сам, когда терминал о нём не заявляет.

// ItalicsEnabled решает, рисовать ли курсив: по настройке или по терминалу.
func ItalicsEnabled(mode, term string) bool {
	switch mode {
	case config.ItalicOn:
		return true
	case config.ItalicOff:
		return false
	default:
		return terminalHasItalics(term)
	}
}

// terminalHasItalics — умеет ли терминал курсив, судя по TERM.
//
// Разбирать терминфо ради одного признака не стоит: в Go его нет в стандартной
// библиотеке, а тянуть зависимость ради одной возможности — дорого. Список
// коротких: screen и его варианты курсив не описывают, всё остальное описывает.
func terminalHasItalics(term string) bool {
	term = strings.ToLower(strings.TrimSpace(term))
	switch {
	case term == "", term == "dumb":
		return false
	case strings.HasPrefix(term, "screen"):
		// Умолчание tmux. Курсив он покажет инверсией — серой заливкой.
		return false
	case strings.HasPrefix(term, "linux"), strings.HasPrefix(term, "vt100"),
		strings.HasPrefix(term, "vt220"), strings.HasPrefix(term, "ansi"):
		return false
	}
	return true
}

// applyItalics включает или выключает курсив в стилях ленты.
// Вызывается один раз при запуске, до первой отрисовки.
func applyItalics(enabled bool) {
	styNotice = styNotice.Italic(enabled)
	styThinking = styThinking.Italic(enabled)
}

// termName возвращает TERM текущего сеанса.
func termName() string { return os.Getenv("TERM") }

// disableItalics снимает курсив со всего стиля glamour: иначе выделение
// в ответе модели (*вот такое*) в том же tmux превратится в серую заливку.
//
// Поле Italic имеет тип *bool и встречается в глубине структуры десятками —
// перечислять их руками значит однажды пропустить новое. Обход отражением
// **заменяет указатель**, а не пишет по нему: базовый стиль glamour общий
// на всю программу, и запись вглубь испортила бы его всем.
func disableItalics(style *ansi.StyleConfig) {
	clearItalicFields(reflect.ValueOf(style).Elem())
}

func clearItalicFields(v reflect.Value) {
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := range t.NumField() {
		field := v.Field(i)
		if !field.CanSet() {
			continue
		}
		if t.Field(i).Name == "Italic" && field.Kind() == reflect.Pointer &&
			field.Type().Elem().Kind() == reflect.Bool {
			no := false
			field.Set(reflect.ValueOf(&no))
			continue
		}
		switch field.Kind() {
		case reflect.Struct:
			clearItalicFields(field)
		case reflect.Pointer:
			if field.IsNil() || field.Elem().Kind() != reflect.Struct {
				continue
			}
			// Указатель ведёт в общую память базового стиля — правим копию
			// и подменяем указатель. Иначе снятый здесь курсив пропал бы
			// у всей программы разом, включая стиль, который никто не просил
			// менять.
			dup := reflect.New(field.Type().Elem())
			dup.Elem().Set(field.Elem())
			clearItalicFields(dup.Elem())
			field.Set(dup)
		}
	}
}
