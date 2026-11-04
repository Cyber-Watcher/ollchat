// Package textx — общие мелочи над текстом: обрезка с многоточием, обрезка
// посередине. Заведён этапом 91 (R8.3): до него пять копий одной обрезки
// жили в агенте, инструментах, обслуживании базы и интерфейсе, и каждая
// считала многоточие по-своему.
package textx

import (
	"fmt"
	"strings"
)

// TermSafe — чужой текст для терминала и ленты TUI: управляющие знаки C0,
// DEL и C1 печатаются видимо (`\x1b`, `\r`); перевод строки и табуляция
// остаются как есть. ESC-последовательность в ответе сервера иначе чистит
// экран, перекрашивает вывод или меняет заголовок окна (OSC), а `\r`
// затирает напечатанное (аудит 23.10.2026). Перенесён сюда из ключей MCP
// (cmd/ollchat, termSafe) аудитом этапа 123: тот же чужой текст — имена
// моделей сервера в отказе /graph-server — шёл в ленту без чистки.
func TermSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0:
			fmt.Fprintf(&b, "\\x%02x", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Shorten обрезает строку до n рун, последней ставя многоточие; короткая
// строка возвращается как есть.
func Shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// ShortenOneLine — то же, но сперва схлопывает пробелы и переводы строк:
// для заголовка команды в ленте, где многострочный текст не нужен.
func ShortenOneLine(s string, n int) string {
	return Shorten(strings.Join(strings.Fields(s), " "), n)
}

// ShortenMiddle обрезает посередине, сохраняя начало и конец: для путей
// и имён файлов, у которых важны и каталог, и расширение.
func ShortenMiddle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	head := n/2 - 1
	tail := n - head - 1
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}
