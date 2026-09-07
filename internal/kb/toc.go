package kb

import (
	"strings"
	"unicode"
)

// LooksLikeTOC — похож ли кусок на оглавление или предметный указатель:
// большинство непустых строк кончается числом (номером страницы).
//
// **Зачем.** Оглавление — худшее подтверждение из возможных: в нём названы
// все понятия книги разом, поэтому по числу упоминаний оно выигрывает у любой
// страницы по делу, а короткому вопросу «configuring CoreDNS» строка
// «Configuring CoreDNS 203» близка и по словам, и по смыслу. Замер 07.09.2026
// (docs/eval/stage98-0907.md): среди 128 подтверждений по 16 вопросам
// оглавлений 12 даже после отбора по смыслу. Решение владельца: в выдаче
// им не место.
//
// Эвристика про строение куска, а не про содержание. Строка оглавления —
// это текст с буквами и **номер страницы на конце**: короткое десятичное число
// (до четырёх знаков) после пробела или точечного отточия. Строка дампа
// памяти или регистров кончается шестнадцатеричным словом («…`4ab7fd58»)
// или значением после «=» («edx=00000»): замер 07.09.2026 на стенограмме
// WinDbg (книга с четвертью таких кусков) — без этого различия она вся
// считалась оглавлением. Порог — четыре строки, чтобы абзац с годом на конце
// не считался оглавлением; половина строк — оглавления «Core Kubernetes»
// дают 59–70%, абзацы по делу 0–10% (privatescripts/tocprobe).
func LooksLikeTOC(text string) bool {
	lines, ends := 0, 0
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		lines++
		if endsWithPageNumber(l) {
			ends++
		}
	}
	return lines >= 4 && ends*2 >= lines
}

// endsWithPageNumber — кончается ли строка номером страницы: до четырёх
// десятичных цифр, перед ними пробел или отточие, а в самой строке есть буквы.
func endsWithPageNumber(l string) bool {
	i := len(l)
	for i > 0 && l[i-1] >= '0' && l[i-1] <= '9' {
		i--
	}
	digits := len(l) - i
	if digits == 0 || digits > 4 || i == 0 {
		return false
	}
	head := l[:i]
	switch {
	case strings.HasSuffix(head, " "), strings.HasSuffix(head, "\t"),
		strings.HasSuffix(head, "."), strings.HasSuffix(head, "…"):
	default:
		return false
	}
	return strings.ContainsFunc(head, unicode.IsLetter)
}
