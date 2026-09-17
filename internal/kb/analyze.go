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
// ru-en-v2 (17.09.2026): слово, перенесённое на новую строку («алго‐\nритмы»),
// склеивается и кладётся в индекс целиком, а его части сохраняются.
const AnalyzerVersion = "ru-en-v2"

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
		// wrapAt — места внутри слова, где стоял перенос строки. Нужны,
		// чтобы положить в индекс не только склеенное слово, но и части.
		wrapAt []int
	)

	flush := func() {
		if len(word) > 0 {
			out = emit(out, word, flags, pos)
			// Слово было собрано через перенос строки: кладём ещё и части,
			// чтобы составное слово («специалистов-практиков») тоже нашлось.
			for _, at := range wrapAt {
				out = emitWrapParts(out, word, at, flags, pos)
			}
			pos++
			word = word[:0]
			wrapAt = wrapAt[:0]
			flags = 0
		}
	}

	runes := []rune(text)
	// skipTo — до какого места пропускать: знак переноса вместе с концом
	// строки не должен закрывать слово, иначе склейки не выйдет.
	skipTo := 0
	for i, r := range runes {
		if i < skipTo {
			continue
		}
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
		case isSoftHyphen(r) && len(word) > 0 && hyphenWrap(runes, i) > 0:
			// Перенос слова на новой строке: «алго‐\nритмы» — это `алгоритмы`.
			//
			// **Зачем.** Такие переносы нарисованы в самих книгах (сторонний
			// pdftotext даёт то же самое, наш разбор тут ни при чём), и до
			// 17.09.2026 слово попадало в индекс двумя обрубками — `алго`
			// и `ритмы`, — а целиком не находилось ни по одному запросу.
			// Замер: переносы есть в 9,29% кусков, 93,6% из них — перенос
			// строки, то есть именно этот случай.
			//
			// **Части тоже сохраняются** — их кладёт emitSplit ниже. Иначе
			// пострадали бы составные слова: «специалистов‐\nпрактиков» —
			// не «специалистовпрактиков», а два слова через дефис, и отличить
			// их от переноса без словаря нельзя. Поэтому в индекс идёт и то
			// и другое: лишний терм дешевле потерянного слова.
			wrapAt = append(wrapAt, len(word))
			flags |= flagWrapped
			skipTo = hyphenWrap(runes, i)
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
	// flagWrapped — слово собрано через перенос строки («алго‐\nритмы»).
	// По нему flush кладёт в индекс ещё и части: перенос и составное слово
	// внешне неразличимы, и терять ни то ни другое нельзя.
	flagWrapped
)

// isSoftHyphen — знаки, которыми набирают перенос: мягкий перенос и
// типографские дефисы. Обычный ASCII-дефис сюда НЕ входит: он бывает частью
// слова («out-of-the-box»), и по нему перенос не опознать.
func isSoftHyphen(r rune) bool {
	switch r {
	case '\u00ad', '\u2010', '\u2011', '\u2043':
		return true
	}
	return false
}

// hyphenWrap — стоит ли знак в конце строки, а за ним продолжение слова.
//
// Возвращает место, с которого слово продолжается (0 — это не перенос).
// Смотрим вперёд: после знака только пробелы и ровно один перевод строки,
// а дальше буква. Пустая строка означает конец абзаца — там переноса нет.
func hyphenWrap(runes []rune, i int) int {
	newlines := 0
	for j := i + 1; j < len(runes) && j < i+12; j++ {
		switch r := runes[j]; {
		case r == '\n':
			newlines++
			if newlines > 1 {
				return 0
			}
		case r == ' ' || r == '\t' || r == '\r':
			// пробелы между знаком и продолжением допустимы
		case isWordRune(r):
			if newlines == 1 {
				return j
			}
			return 0
		default:
			return 0
		}
	}
	return 0
}

// emitWrapParts кладёт в индекс части слова, собранного через перенос.
//
// Само слово уже положено целиком («алгоритмы»); здесь добавляются куски
// («алго», «ритмы») — на случай, когда это было не перенос, а составное
// слово, разорванное по дефису: «специалистов-практиков». Позиция у частей
// та же, что у целого: они стоят на одном месте текста.
func emitWrapParts(out []Token, word []rune, at int, flags wordFlags, pos uint32) []Token {
	if at <= 0 || at >= len(word) {
		return out
	}
	head, tail := word[:at], word[at:]
	if len(head) >= minTermRunes {
		out = emit(out, head, flags, pos)
	}
	if len(tail) >= minTermRunes {
		out = emit(out, tail, flags, pos)
	}
	return out
}

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
