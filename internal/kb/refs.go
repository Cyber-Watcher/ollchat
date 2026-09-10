package kb

import (
	"strings"
	"unicode"
)

// Список литературы и выходные данные книги: распознать, чтобы не разбирать.
//
// **Откуда взялось.** Разбор понятий без связей 09.09.2026 (этап 101, Г6):
// 11 тысяч понятий графа (4%) не имеют ни одной связи, и по именам видно, откуда
// они взялись — `Alan Kay`, `Smith, J.`, `Dzmitry Bahdanau` из списков литературы,
// `Adobe Minion Pro`, `Guardian Sans` из колофона («книга набрана шрифтом…»).
// Отношений в таком тексте нет по его устройству: это перечень, а не рассказ.
//
// Оглавления мы уже не разбираем (`LooksLikeTOC`, этап 99). Здесь — тот же приём
// для двух других видов служебного текста.
//
// **Осторожность важнее полноты.** Ошибка первого рода дорога: выбросив главу,
// мы теряем знание навсегда и молча. Поэтому признаки строгие, и каждая
// эвристика проверяется переписью по всей коллекции с осмотром худшей книги —
// правило, оплаченное дефектом `LooksLikeTOC` (07.09.2026: дампы WinDbg были
// приняты за оглавление).

// LooksLikeRefs — похож ли кусок на список литературы.
//
// Признак строки-ссылки: год в скобках («(2019)»), DOI, arXiv, инициалы автора
// («I. Goodfellow», «Klein, P. N.»), «pp.»/«vol.»/«et al.». Нужно, чтобы такими
// была **половина** строк куска и строк было не меньше пяти.
//
// **Почему так строго и почему без ссылок на сеть.** Первая редакция считала
// признаком любую строку с http(s) и брала половину строк — и на переписи
// 09.09.2026 поймала списки полезных ресурсов из «Learning Angular» и
// «Modern Web Development with Angular»: маркированный перечень сообществ и
// репозиториев выглядит как библиография построчно, но это содержательный текст
// главы. Ссылка на сеть выброшена из признаков, порог поднят. Правило то же,
// что спасло нас на оглавлениях: эвристику проверять переписью и смотреть
// глазами худшие книги, а не пару примеров.
func LooksLikeRefs(text string) bool {
	lines, refs := 0, 0
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		lines++
		if looksLikeRefLine(l) {
			refs++
		}
	}
	return lines >= 5 && refs*2 >= lines
}

// refHeads — заголовки, которыми открывается список литературы.
// Проверяются по началу строки, в нижнем регистре.
var refHeads = []string{
	"references", "bibliography", "works cited", "further reading",
	"literature cited", "список литературы", "список источников",
	"библиография", "литература", "источники",
}

// RefsHeading — начинается ли кусок с заголовка списка литературы.
//
// Отдельно от `LooksLikeRefs`, потому что первый кусок раздела часто содержит
// сам заголовок и две-три записи — строк мало, доля не набирается, а раздел
// уже начался. В признак FlagRefs не входит: перепись по коллекции для неё
// не делалась (правило «эвристику проверять переписью»), пока её зовёт только
// пробник refsprobe.
func RefsHeading(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" {
			continue
		}
		// Заголовок стоит отдельной короткой строкой; «references» внутри
		// предложения заголовком не считается.
		if len(l) > 40 {
			return false
		}
		l = strings.Trim(l, " .:*#—-")
		for _, h := range refHeads {
			if l == h {
				return true
			}
		}
		return false
	}
	return false
}

// colophonMarks — обороты выходных данных и колофона. Каждый сам по себе
// встречается и в обычном тексте, поэтому решает не один, а их число.
var colophonMarks = []string{
	"all rights reserved", "printed in the united states",
	"no part of this book may be reproduced", "isbn",
	"the cover designer", "cover designer", "the cover image",
	"typeface", "typefaces", "the text font", "the heading font",
	"set in", "interior designer", "проприетарные права", "все права защищены",
}

// LooksLikeColophon — похож ли кусок на выходные данные или колофон.
//
// Такой кусок даёт графу названия шрифтов и имена художников как «понятия»,
// а знания в нём нет никакого. Признаком служит **число** оборотов: одиночное
// «ISBN» встречается в тексте о книгоиздании, три оборота разом — нет.
func LooksLikeColophon(text string) bool {
	low := strings.ToLower(text)
	hits := 0
	for _, m := range colophonMarks {
		if strings.Contains(low, m) {
			hits++
		}
	}
	return hits >= 3
}

// looksLikeRefLine — похожа ли строка на библиографическую запись.
//
// Решает НАЧАЛО строки, а не её содержимое. Запись открывается автором или
// номером источника: «Abbeel, P. and Ng, A. Y. (2004)…», «[12] Traag V.…»,
// «1. Fortune, "AI-powered coding tool"…». Проза начинается с любого слова.
//
// **Почему не по содержимому.** Вторая редакция считала признаком год в скобках
// где угодно в строке — и на переписи 09.09.2026 взяла 331 кусок «Artificial
// Intelligence: A Modern Approach». Это не библиография: у книги академический
// стиль ссылок, и «(Russell and Norvig, 2020)» стоит чуть ли не в каждом абзаце
// основного текста. Признак, который срабатывает на прозе целой книги, —
// не признак.
func looksLikeRefLine(l string) bool {
	low := strings.ToLower(l)
	switch {
	case strings.Contains(low, "doi:"), strings.Contains(low, "doi.org"),
		strings.Contains(low, "arxiv:"):
		return strings.ContainsFunc(l, unicode.IsLetter)
	}
	return startsLikeRefEntry(l)
}

// startsLikeRefEntry — открывается ли строка так, как открывается запись
// в списке литературы.
func startsLikeRefEntry(l string) bool {
	// «[12] …» — нумерованный список источников.
	if strings.HasPrefix(l, "[") {
		if i := strings.IndexByte(l, ']'); i > 1 && allDigits(l[1:i]) {
			return strings.ContainsFunc(l[i:], unicode.IsLetter)
		}
		return false
	}

	fields := strings.Fields(l)
	if len(fields) < 2 {
		return false
	}
	// «1. Fortune, …» — номер записи; дальше должно идти то же, что и без него.
	if n := strings.TrimSuffix(fields[0], "."); n != fields[0] && allDigits(n) && len(n) <= 3 {
		fields = fields[1:]
		if len(fields) < 2 {
			return false
		}
	}

	// Автор пишется двояко: «Goodfellow, I.» и «I. Goodfellow». И то и другое
	// даёт инициал в зачине записи; в прозе инициалов там нет.
	//
	// Фамилия с запятой перед инициалом — сама по себе достаточный зачин
	// («Girvan, M., & Newman, M. E. J. …»): второго инициала в первых словах
	// может не оказаться, а запись это именно она.
	head := strings.Join(fields[:min(6, len(fields))], " ")
	surnameThenInitial := strings.HasSuffix(fields[0], ",") && isInitial(fields[1])
	if !surnameThenInitial && !hasInitials(head) {
		return false
	}
	// У записи есть ещё и выходные данные: год, страницы, издание.
	low := strings.ToLower(l)
	if hasBracketYear(l) || hasBareYear(l) {
		return true
	}
	for _, m := range []string{" pp. ", " vol. ", " eds.", " ed.)", "et al.", "press", "journal", "proceedings"} {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// hasBareYear — год без скобок: «… Scientific Reports, 9, 5233, 2019.»
func hasBareYear(l string) bool {
	for _, w := range strings.Fields(l) {
		w = strings.Trim(w, ".,;:()[]")
		if len(w) == 4 && allDigits(w) && (w[0] == '1' || w[0] == '2') {
			return true
		}
	}
	return false
}

// hasBracketYear — есть ли в строке год в скобках: «(2019)», «(1999a)».
func hasBracketYear(l string) bool {
	for i := 0; i+5 < len(l)+1; i++ {
		if l[i] != '(' {
			continue
		}
		rest := l[i+1:]
		if len(rest) < 5 {
			return false
		}
		if !allDigits(rest[:4]) {
			continue
		}
		y := rest[:4]
		if y[0] != '1' && y[0] != '2' {
			continue
		}
		switch rest[4] {
		case ')', ',', ';':
			return true
		default:
			// «(2019a)» — тоже год, если следом скобка.
			if len(rest) > 5 && rest[5] == ')' && rest[4] >= 'a' && rest[4] <= 'z' {
				return true
			}
		}
	}
	return false
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// hasInitials — есть ли в строке инициалы автора: «I. Goodfellow», «Klein, P. N.»,
// «Hamilton WL, Ying R». Признак библиографической записи, которого нет
// в обычной прозе: там имя пишется целиком.
//
// Считается одиночная заглавная буква с точкой после неё, стоящая отдельным
// словом. Одной мало — так пишут сокращения вроде «п. 3»; нужно две.
func hasInitials(l string) bool {
	n := 0
	for _, w := range strings.Fields(l) {
		if isInitial(w) {
			n++
			if n >= 2 {
				return true
			}
		}
	}
	return false
}

// isInitial — слово вида «И.» или «P.»: одна заглавная буква с точкой.
func isInitial(w string) bool {
	w = strings.Trim(w, "(),;&")
	r := []rune(w)
	return len(r) == 2 && unicode.IsUpper(r[0]) && r[1] == '.'
}
