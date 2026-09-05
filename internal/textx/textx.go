// Package textx — общие мелочи над текстом: обрезка с многоточием, обрезка
// посередине. Заведён этапом 91 (R8.3): до него пять копий одной обрезки
// жили в агенте, инструментах, обслуживании базы и интерфейсе, и каждая
// считала многоточие по-своему.
package textx

import "strings"

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
