package maint

import (
	"os"
	"strings"
	"unicode"

	"golang.org/x/term"
)

// Помощники, которые до этапа 91 (R4) делили один пакет main с командами
// базы знаний. Копии; уборка дублирования — R8.

func dashes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}

// isTTY отвечает, смотрит ли человек в терминал.
//
// От этого зависят и цвет, и строка хода: `--kb-doctor books > файл.txt`
// должен давать чистый текст, а не управляющие последовательности.
//
// **Проверка настоящая, через ioctl, а не по типу файла.** `os.ModeCharDevice`
// выставлен и у `/dev/null`, а это ровно тот случай, когда терминала нет:
// `--kb-merge books < /dev/null` выглядел бы для программы разговором с
// человеком. Для цвета такая ошибка стоит мусора в файле, а для подтверждения
// необратимого действия — гораздо дороже. Поймано своим же тестом 30.08.2026.
func isTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// shellQuote берёт слово в одинарные кавычки, если без них оболочка его
// разрежет или истолкует: команды, которые печатают доктор и чистки,
// копируют в терминал как есть, и путь «/Machine Learning» без кавычек
// превращался в два довода, а «(корень)» — в синтаксическую ошибку bash.
// Буквы любого алфавита и обычные знаки пути кавычек не требуют.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("/._-+,:@%=", r) {
			continue
		}
		safe = false
		break
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// bar рисует полосу заполнения.
//
// Знаками рамки, а не «#»: они одной ширины в любом моноширинном шрифте
// и не сливаются с текстом вокруг. Ширина невелика нарочно — строка несёт
// ещё скорость, остаток и имя книги, и полоса не должна их вытеснять.
func bar(pct int) string {
	const width = 20
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	full := pct * width / 100
	return "[" + strings.Repeat("█", full) + strings.Repeat("░", width-full) + "]"
}
