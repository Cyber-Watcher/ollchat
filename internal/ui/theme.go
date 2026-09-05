package ui

import (
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"strings"

	"charm.land/glamour/v2/ansi"
	glamourstyles "charm.land/glamour/v2/styles"
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Оформление markdown: встроенный стиль glamour плюс настройки из конфига.
//
// Своё оформление понадобилось из-за двух вещей во встроенном тёмном стиле.
// Инлайновый код в нём — ANSI 203 на заливке 236, то есть ровно тот красный,
// которым в ленте нарисованы ошибки: код в тексте читается как ругань.
// А заливка блока кода (#373737) вырезает прямоугольник в фоне терминала —
// у кого обои или прозрачность, это видно сразу.

// themePrefix — приставка к имени производной темы chroma.
const themePrefix = "ollchat-"

// buildStyle собирает стиль glamour: берёт встроенный за основу и накладывает
// поверх настройки раздела [theme].
//
// Встроенный стиль правится только заменой указателей, без разыменования:
// styles.DefaultStyles отдаёт указатель на пакетную переменную, общую на всю
// программу, и запись вглубь неё испортила бы стиль всем остальным.
func buildStyle(th config.Theme) (ansi.StyleConfig, error) {
	base, ok := glamourstyles.DefaultStyles[th.Style]
	if !ok {
		return ansi.StyleConfig{}, fmt.Errorf("неизвестный стиль оформления %q", th.Style)
	}
	out := *base

	// Инлайновый код. Пустая настройка означает «как в базовом стиле»
	// для цвета и «без заливки» для фона: фон убирается намеренно.
	if th.InlineCode != "" {
		out.Code.Color = strPtr(th.InlineCode)
	}
	out.Code.BackgroundColor = strPtrOrNil(th.InlineCodeBG)

	// Блоки кода. Поле Theme действует, только когда не задан Chroma:
	// иначе glamour молча подменяет тему на свою «charm», и настройка
	// выглядит рабочей, ничего не меняя.
	switch {
	case th.CodeTheme != "":
		out.CodeBlock.Chroma = nil
		out.CodeBlock.Theme = codeTheme(th.CodeTheme, th.CodeBG == "", th.Tokens)
	case base.CodeBlock.Chroma != nil && th.CodeBG == "":
		// Тема не задана — оставляем цвета базового стиля, но снимаем
		// его заливку. Копируем структуру целиком по той же причине,
		// по которой не пишем вглубь базового стиля.
		ch := *base.CodeBlock.Chroma
		ch.Background = ansi.StylePrimitive{}
		out.CodeBlock.Chroma = &ch
	}
	out.CodeBlock.BackgroundColor = strPtrOrNil(th.CodeBG)

	return out, nil
}

// codeTheme возвращает имя темы chroma для блоков кода. Если нужно снять
// заливку или перекрасить отдельные токены, регистрируется производная тема.
//
// Регистрация идёт в глобальный список тем chroma, поэтому имя производной
// зависит от всего, что её отличает: с одним и тем же именем второй набор
// правок молча получил бы первую тему. Список — обычная карта без блокировки,
// но и создание рендерера, и отрисовка идут в одной горутине цикла событий.
func codeTheme(name string, transparent bool, tokens map[string]string) string {
	if !transparent && len(tokens) == 0 {
		return name
	}
	derived := derivedName(name, transparent, tokens)
	if _, ok := chromastyles.Registry[derived]; ok {
		return derived
	}

	sb := chromastyles.Get(name).Builder()
	if transparent {
		sb.Transform(func(e chroma.StyleEntry) chroma.StyleEntry {
			e.Background = 0 // 0 — «цвет не задан», см. chroma.Colour.IsSet
			return e
		})
	}
	for token, colour := range tokens {
		tt, err := chroma.TokenTypeString(token)
		if err != nil {
			continue // имя проверено в конфиге, сюда попасть не должно
		}
		// Меняем только цвет: жирность и курсив у токена задавала тема,
		// и терять их из-за смены цвета незачем.
		e := sb.Get(tt)
		e.Colour = chroma.ParseColour(colour)
		sb.AddEntry(tt, e)
	}

	built, err := sb.Build()
	if err != nil {
		// Производная не собралась — пусть будет исходная тема:
		// это лучше, чем ответ без подсветки вовсе.
		return name
	}
	// Имя обязательно менять до записи в список: Builder уносит имя исходной
	// темы, и производная затёрла бы её саму.
	built.Name = derived
	chromastyles.Register(built)
	return derived
}

// derivedName собирает имя производной темы так, чтобы разные наборы правок
// не делили одну запись в общем списке тем.
func derivedName(name string, transparent bool, tokens map[string]string) string {
	var sig strings.Builder
	if transparent {
		sig.WriteString("nobg;")
	}
	for _, token := range slices.Sorted(maps.Keys(tokens)) {
		sig.WriteString(token)
		sig.WriteString("=")
		sig.WriteString(tokens[token])
		sig.WriteString(";")
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(sig.String()))
	return fmt.Sprintf("%s%s-%08x", themePrefix, name, h.Sum32())
}

func strPtr(s string) *string { return &s }

// strPtrOrNil отдаёт nil для пустой строки: в стиле glamour nil означает
// «свойство не задано», то есть цвет терминала.
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
