// Package kb — база знаний по книгам: индекс, поиск, хранилище кусков текста.
package kb

import (
	"strings"
	"unicode"
)

// Разбор текста на термы.
//
// Здесь решается, найдётся ли книга по запросу, поэтому правила выбраны под
// технические тексты, а не под художественные:
//
//   - Идентификаторы не режутся. `sync.WaitGroup`, `go.mod`, `--cap-add`, `C++`
//     остаются целыми: половина запросов к техническим книгам — это точные
//     имена, и разрезанное на части имя не найдётся никогда.
//   - Тот же идентификатор дополнительно кладётся в индекс по частям и на той
//     же позиции: тогда «http-клиент» найдёт `HTTPClient`.
//   - Идентификаторы не приводятся к основе. `Kubernetes` не должен стать
//     `kubernet`, иначе он перестанет совпадать с `kubernetes.io`.
//   - Обычные слова приводятся к основе, причём правило выбирается по алфавиту
//     самого слова, а не по языку книги: в русской книге про Go половина слов
//     латиницей.
//   - Стоп-слова не выбрасываются: без них рассыпается поиск устойчивых
//     сочетаний вроде «in memory» или «по значению». Слишком частые термы
//     отсекаются позже, при разборе запроса.
//
// Отдельно про источник текста: куски обязаны браться из document.Parts,
// а не из склеенного document.Doc.Text. Проверка на живой книге показала, что
// иначе в двадцатку самых частых термов выходит «страниц» — из наших же
// заголовков «── страница N ──», по одному на каждую страницу. Такой терм
// ничего не значит, но портит ранжирование: он делает слово «страница»
// бесполезно частым.
//
// Версия правил пишется в описание коллекции. Если она разойдётся с текущей,
// коллекцию надо пересобрать — но только сегменты, тексты уже извлечены.

// AnalyzerVersion — версия правил разбора. Меняется вместе с правилами.
const AnalyzerVersion = "ru-en-v1"

const (
	minTermRunes = 2
	maxTermRunes = 40
)

// Token — терм и его позиция в тексте, считая в термах.
type Token struct {
	Term string
	Pos  uint32
}

// Tokens разбирает текст на термы. Срез out переиспользуется вызывающим кодом:
// кусков в библиотеке миллионы, и выделение памяти на каждый заметно.
func Tokens(text string, out []Token) []Token {
	out = out[:0]
	var (
		word  []rune
		flags wordFlags
		pos   uint32
	)

	flush := func() {
		if len(word) > 0 {
			out = emit(out, word, flags, pos)
			pos++
			word = word[:0]
			flags = 0
		}
	}

	runes := []rune(text)
	for i, r := range runes {
		switch {
		case isWordRune(r):
			if len(word) < maxTermRunes*2 {
				word = append(word, r)
			}
			flags |= classify(r)
			// Внутренняя заглавная посреди слова — признак имени вроде httpClient.
			if unicode.IsUpper(r) && len(word) > 1 && unicode.IsLower(runes[i-1]) {
				flags |= flagCamel
			}
		case isConnector(r) && len(word) > 0 && i+1 < len(runes) && isWordRune(runes[i+1]):
			// Разделитель внутри слова оставляем: это часть имени.
			word = append(word, r)
			flags |= flagConnector
		case (r == '+' || r == '#') && len(word) > 0 && isTail(runes[i-1]):
			// Знак сразу за словом — часть названия: C++, C#, F#, R#.
			// Пробел перед ним всё меняет: «a + b» — это сложение, и там
			// слово уже закрыто предыдущей веткой.
			word = append(word, r)
			flags |= flagConnector
		default:
			flush()
		}
	}
	flush()
	return out
}

type wordFlags uint8

const (
	flagLetter wordFlags = 1 << iota
	flagDigit
	flagCyrillic
	flagLatin
	flagCamel
	flagConnector
)

func classify(r rune) wordFlags {
	var f wordFlags
	switch {
	case unicode.IsDigit(r):
		f |= flagDigit
	case unicode.IsLetter(r):
		f |= flagLetter
		if r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' || r == 'ё' || r == 'Ё' {
			f |= flagCyrillic
		} else if r < 128 {
			f |= flagLatin
		}
	}
	return f
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// isTail сообщает, что знак может продолжить уже начатое слово: сама буква,
// цифра или такой же знак — как второй плюс в «C++».
func isTail(r rune) bool { return isWordRune(r) || r == '+' || r == '#' }

// isConnector перечисляет знаки, которые внутри слова являются его частью.
func isConnector(r rune) bool {
	switch r {
	case '.', '_', '-', '+', '#', '/':
		return true
	}
	return false
}

// emit кладёт в индекс сам терм и, если это имя, его части.
func emit(out []Token, word []rune, flags wordFlags, pos uint32) []Token {
	term := normalize(word)
	if term == "" {
		return out
	}

	// Имя — это слово с разделителем внутри, со смешением букв и цифр либо
	// с заглавной посреди слова.
	ident := flags&flagConnector != 0 ||
		flags&flagCamel != 0 ||
		(flags&flagDigit != 0 && flags&flagLetter != 0)

	if !ident {
		if t := stem(term, flags); t != "" {
			out = append(out, Token{Term: t, Pos: pos})
		}
		return out
	}

	if fits(term) {
		out = append(out, Token{Term: term, Pos: pos})
	}
	// Части имени — на той же позиции: запрос «http клиент» должен находить
	// HTTPClient, а «go mod» — go.mod.
	for _, part := range splitIdent(term) {
		if part != term && fits(part) {
			out = append(out, Token{Term: part, Pos: pos})
		}
	}
	return out
}

// normalize приводит слово к нижнему регистру и снимает различие ё/е.
func normalize(word []rune) string {
	var b strings.Builder
	b.Grow(len(word))
	for _, r := range word {
		r = unicode.ToLower(r)
		if r == 'ё' {
			r = 'е'
		}
		b.WriteRune(r)
	}
	s := b.String()
	// Разделители в начале смысла не несут: «--cap-add» → «cap-add».
	s = strings.TrimLeft(s, "._-+#/")
	// А в конце несут не все: «C++», «C#», «F#» — это имена языков, и знак
	// в них часть названия. Точку, дефис и слеш убираем, плюс и решётку — нет.
	return strings.TrimRight(s, "._-/")
}

func fits(term string) bool {
	n := len([]rune(term))
	return n >= minTermRunes && n <= maxTermRunes
}

// splitIdent разбивает имя на части: по разделителям и по границам регистра.
func splitIdent(term string) []string {
	var parts []string
	for _, chunk := range strings.FieldsFunc(term, isConnector) {
		if chunk != "" {
			parts = append(parts, chunk)
		}
	}
	if len(parts) == 1 && parts[0] == term {
		return nil
	}
	return parts
}

// stem приводит обычное слово к основе по алфавиту самого слова.
func stem(term string, flags wordFlags) string {
	if !fits(term) {
		return ""
	}
	switch {
	case flags&flagCyrillic != 0:
		return stemRussian(term)
	case flags&flagLatin != 0:
		return stemEnglish(term)
	}
	return term
}
