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
//
// ru-en-v2 (17.09.2026, в индекс не попала): склейка слова, перенесённого
// на новую строку знаком U+00AD или U+2010.
// ru-en-v3 (17.09.2026): то же для обычного дефиса — им переносит большинство
// вёрсток, и перепись всей библиотеки насчитала таких переносов 311 тысяч
// в 27% кусков против 94 тысяч у v2; лигатуры «ﬁ», «ﬂ» раскрываются в буквы;
// ударение и разложенные «й», «ё» не рвут слово; невидимые знаки нулевой
// ширины пропускаются; мягкий перенос внутри слова слово не рвёт.
// Старый индекс с новыми правилами совместим: в запросах переносов нет.
// Пересобрать его без перечитывания книг — Collection.Reanalyze.
const AnalyzerVersion = "ru-en-v3"

const (
	minTermRunes = 2
	maxTermRunes = 64
)

// Token — терм и его место в тексте.
type Token struct {
	Term string
	Pos  uint32
	// Start и End — границы слова в рунах исходного текста. У слова,
	// собранного через перенос строки, они охватывают обе половины: по ним
	// выдержка находит «алго-\nритмы» по запросу «алгоритмы».
	Start, End int
}

// wrapLookahead — как далеко за знаком переноса искать продолжение слова.
// Отступ следующей строки в раскладке PDF бывает в десятки пробелов.
const wrapLookahead = 120

// Tokens разбирает текст на термы. Срез out переиспользуется между вызовами.
func Tokens(text string, out []Token) []Token {
	out = out[:0]
	var (
		word  []rune
		flags wordFlags
		pos   uint32
		start int
		// joins — места внутри слова, где стоял перенос. Нужны, чтобы положить
		// в индекс не только склеенное слово, но и его части.
		joins []int
	)
	runes := []rune(text)

	flush := func(end int) {
		if len(word) == 0 {
			return
		}
		first := len(out)
		out = emit(out, word, flags, pos)
		if len(joins) > 0 {
			out = emitJoinedParts(out, first, word, joins, pos)
		}
		for k := first; k < len(out); k++ {
			out[k].Start, out[k].End = start, end
		}
		pos++
		word, joins, flags = word[:0], joins[:0], 0
	}
	add := func(i int, r rune) {
		if len(word) == 0 {
			start = i
		}
		if len(word) < maxTermRunes*2 {
			word = append(word, r)
		}
		flags |= classify(r)
	}

	// skipTo — до какого места пропускать: знак переноса вместе с концом
	// строки и отступом не должен закрывать слово, иначе склейки не выйдет.
	skipTo := 0
	for i, r := range runes {
		if i < skipTo {
			continue
		}
		switch {
		case ligature(r) != "":
			// Стоит раньше ветки букв: для юникода лигатура — тоже буква.
			// «conﬁgured» в книге и «configured» в запросе — одно слово.
			// Лигатуры приходят из шрифтов PDF: перепись 17.09.2026 нашла их
			// в 76 книгах, в отдельных — в трёх кусках из четырёх.
			for _, x := range ligature(r) {
				add(i, x)
			}
		case isWordRune(r):
			add(i, r)
			// Внутренняя заглавная посреди слова — признак имени вроде httpClient.
			if unicode.IsUpper(r) && len(word) > 1 && unicode.IsLower(runes[i-1]) {
				flags |= flagCamel
			}
		case isCombining(r):
			// Ударение («замка́ми») и разложенные «й», «ё» (и + U+0306) слово
			// не рвут: знак либо сливается с буквой, либо отбрасывается.
			if n := len(word); n > 0 {
				word[n-1] = compose(word[n-1], r)
			}
		case isZeroWidth(r):
			// Невидимый знак возможного разрыва: вёрстка ставит его внутрь
			// длинных адресов и имён. В слове его нет.
		case isHyphen(r) && len(word) > 0:
			if next := wrapTarget(runes, i); next > 0 {
				// Перенос слова на новую строку: «алго-\nритмы» — `алгоритмы`.
				//
				// **Зачем.** Переносы нарисованы в самих книгах (сторонний
				// pdftotext даёт то же самое), и слово попадало в индекс двумя
				// обрубками — `алго` и `ритмы`, — а целиком не находилось.
				//
				// **Части тоже сохраняются** — их кладёт emitJoinedParts.
				// «специалистов-\nпрактиков» — не «специалистовпрактиков»,
				// а два слова через дефис, и отличить их от переноса без
				// словаря нельзя. Лишний терм дешевле потерянного слова.
				joins = append(joins, len(word))
				skipTo = next
				break
			}
			if wordFollows(runes, i) {
				if r == '\u00ad' {
					// Мягкий перенос посреди строки. В EPUB он невидим и стоит
					// внутри слова; в PDF так нередко записан обычный дефис
					// («пул\u00adреквесты»). Оба случая покрывает одно правило:
					// слово целиком и его части.
					joins = append(joins, len(word))
					break
				}
				// Типографский дефис — тот же дефис: «five‐step» = «five-step».
				word = append(word, '-')
				flags |= flagConnector
				break
			}
			flush(i)
		case isConnector(r) && len(word) > 0 && wordFollows(runes, i):
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
			flush(i)
		}
	}
	flush(len(runes))
	return out
}

type wordFlags uint8

const (
	flagDigit wordFlags = 1 << iota
	flagLetter
	flagCyrillic
	flagLatin
	flagCamel
	flagConnector
)

// isHyphen — знаки, которыми набирают дефис и перенос: обычный дефис, мягкий
// перенос и типографские дефисы.
func isHyphen(r rune) bool {
	switch r {
	case '-', '\u00ad', '\u2010', '\u2011', '\u2043':
		return true
	}
	return false
}

// isZeroWidth — невидимые знаки нулевой ширины.
func isZeroWidth(r rune) bool {
	return (r >= '\u200b' && r <= '\u200d') || r == '\u2060' || r == '\ufeff'
}

// wordFollows сообщает, что сразу за знаком i слово продолжается. Невидимые
// знаки нулевой ширины не в счёт: «cloud.<ZWSP>google.com» — одно имя.
func wordFollows(runes []rune, i int) bool {
	for j := i + 1; j < len(runes); j++ {
		if isZeroWidth(runes[j]) {
			continue
		}
		return isWordRune(runes[j]) || ligature(runes[j]) != ""
	}
	return false
}

// isCombining — знаки, надставляемые над предыдущей буквой.
func isCombining(r rune) bool { return r >= '\u0300' && r <= '\u036f' }

// compose сливает букву с надставным знаком там, где получается другая буква
// русского алфавита; в остальных случаях знак — ударение, и он отбрасывается.
func compose(base, mark rune) rune {
	switch {
	case mark == '\u0306' && base == 'и':
		return 'й'
	case mark == '\u0306' && base == 'И':
		return 'Й'
	case mark == '\u0308' && base == 'е':
		return 'ё'
	case mark == '\u0308' && base == 'Е':
		return 'Ё'
	}
	return base
}

// ligature раскрывает типографскую лигатуру в буквы; для прочих знаков — "".
func ligature(r rune) string {
	switch r {
	case '\ufb00':
		return "ff"
	case '\ufb01':
		return "fi"
	case '\ufb02':
		return "fl"
	case '\ufb03':
		return "ffi"
	case '\ufb04':
		return "ffl"
	case '\ufb05', '\ufb06':
		return "st"
	}
	return ""
}

// script относит букву к алфавиту: перенос не соединяет русское с латинским.
func script(r rune) wordFlags {
	return classify(r) & (flagCyrillic | flagLatin)
}

// wrapTarget — стоит ли знак i в конце строки, а за ним продолжение слова.
//
// Возвращает место, с которого слово продолжается (0 — это не перенос).
// После знака допустимы только пробелы и ровно один перевод строки, дальше —
// отступ и буква. Пустая строка означает конец абзаца: там переноса нет.
//
// Перед знаком и после перевода строки должны стоять буквы одного алфавита:
// «AI-\nассистент» и «в 2020-\nгоду» — не переносы. Обычному дефису веры
// меньше, чем мягкому переносу (им же пишут составные слова и ключи команд),
// поэтому продолжение после него обязано быть строчной буквой: «Embry-\nRiddle»
// остаётся двумя словами.
func wrapTarget(runes []rune, i int) int {
	if i == 0 || !unicode.IsLetter(runes[i-1]) {
		return 0
	}
	newlines := 0
	for j := i + 1; j < len(runes) && j < i+wrapLookahead; j++ {
		switch r := runes[j]; {
		case r == '\n':
			newlines++
			if newlines > 1 {
				return 0
			}
		case r == ' ' || r == '\t' || r == '\r':
			// пробелы между знаком и продолжением допустимы
		case unicode.IsLetter(r):
			if newlines != 1 || script(r) == 0 || script(r) != script(runes[i-1]) {
				return 0
			}
			if runes[i] == '-' && !unicode.IsLower(r) {
				return 0
			}
			return j
		default:
			return 0
		}
	}
	return 0
}

// emitJoinedParts кладёт в индекс части слова, собранного через перенос.
//
// Само слово уже положено целиком («алгоритмы»); здесь добавляются куски
// («алго», «ритмы») — на случай, когда это был не перенос, а составное
// слово, разорванное по дефису. Позиция у частей та же, что у целого: они
// стоят на одном месте текста. Признаки (алфавит, цифры, разделители) у каждой
// части свои: от них зависит, приводится ли она к основе. Термы, уже
// положенные на эту позицию, не повторяются — иначе частота слова в куске
// оказалась бы завышена.
func emitJoinedParts(out []Token, first int, word []rune, joins []int, pos uint32) []Token {
	from := 0
	for k := 0; k <= len(joins); k++ {
		to := len(word)
		if k < len(joins) {
			to = joins[k]
		}
		if to > len(word) {
			to = len(word)
		}
		if part := word[from:to]; len(part) >= minTermRunes {
			out = emit(out, part, flagsOf(part), pos)
		}
		from = to
	}
	// Повторы на одной позиции убираем, порядок сохраняем.
	kept := out[:first]
	for _, t := range out[first:] {
		dup := false
		for _, have := range kept[first:] {
			if have.Term == t.Term {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, t)
		}
	}
	return kept
}

// flagsOf считает признаки слова по его знакам — так же, как их копит Tokens.
func flagsOf(word []rune) wordFlags {
	var f wordFlags
	for i, r := range word {
		f |= classify(r)
		if !isWordRune(r) {
			f |= flagConnector
		}
		if i > 0 && unicode.IsUpper(r) && unicode.IsLower(word[i-1]) {
			f |= flagCamel
		}
	}
	return f
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
