package pdf

import (
	"strings"
	"unicode"
)

// Размеченное содержимое: ActualText.
//
// Вёрстка нередко рисует одно, а читать велит другое. Стандарт PDF даёт для
// этого свойство ActualText у блока BDC…EMC: всё нарисованное внутри блока
// при извлечении текста заменяется на записанную строку. Так оформляют
// буквицы, лигатуры, переносы (нарисован дефис, читать U+00AD) — и пробелы:
//
//	/Span <</ActualText<FEFF0020>>> BDC … [(x)]TJ EMC
//
// **Чем это стоило.** До 17.09.2026 блоки не разбирались вовсе, и в книгах,
// где пробел между словами нарисован глифом с ActualText «пробел», текст
// выходил как «Систему.Java.регламентируют»: глиф по таблице шрифта читается
// точкой. Три русские книги были испорчены на 67–89% кусков — поиск по словам,
// извлечение понятий и векторы разом. Сторонний pdftotext ActualText понимает,
// поэтому у него те же страницы чистые; по этому расхождению причина и нашлась.
//
// Правило: внутри блока с ActualText нарисованное придерживается, а при
// закрытии блока на место первого нарисованного знака кладётся строка
// из ActualText шириной во всё нарисованное. Вложенные блоки подчиняются
// внешнему: заменяет текст самый наружный ActualText.
//
// **Замене верят не всегда.** Издательские конвейеры пишут ActualText
// с ошибками: в книгах Apress капитель колонтитула размечена как
// «нарисовано TEN, читать t», и «Table of Contents» выходит как «Table of
// Contt» — у pdftotext тоже (сверка на 55 книгах 17.09.2026). Замена, в которой
// букв и цифр меньше, чем нарисовано, правдой быть не может: лигатура
// раскрывается в большее число букв, капитель — в то же, пробел и перенос
// заменяют знаки препинания. В таком случае остаётся нарисованное.

// mcSpan — один открытый блок размеченного содержимого.
type mcSpan struct {
	actual  bool   // у блока есть ActualText
	text    string // чем заменить нарисованное
	started bool   // внутри уже что-то нарисовано
	x, y    float64
	size    float64
	endX    float64 // правый край нарисованного
	rawLen  int     // длина ActualText до чистки: 0 — явно пустая замена
	held    []frag  // нарисованное внутри блока: вернётся, если замене нет веры
}

// openSpan открывает блок. props — словарь свойств BDC или nil у BMC.
func (e *extractor) openSpan(props Dict) {
	sp := mcSpan{}
	if s, ok := e.doc.Resolve(props["ActualText"]).(String); ok && props != nil {
		sp.actual = true
		raw := decodeTextString(s)
		sp.rawLen = len(raw)
		sp.text = cleanActual(raw)
	}
	e.spans = append(e.spans, sp)
}

// closeSpan закрывает самый внутренний блок и, если он заменял текст,
// кладёт замену куском на место нарисованного.
func (e *extractor) closeSpan() {
	n := len(e.spans)
	if n == 0 {
		return // лишний EMC в повреждённом потоке
	}
	sp := e.spans[n-1]
	e.spans = e.spans[:n-1]
	if !sp.actual || e.replacing() >= 0 {
		// Либо блок ничего не заменял, либо он вложен в другой заменяющий —
		// тогда слово за внешним.
		return
	}
	if !sp.started {
		// Блок, внутри которого ничего не нарисовано, поставить некуда.
		return
	}
	var drawn strings.Builder
	for _, f := range sp.held {
		drawn.WriteString(f.text)
	}
	if sp.rawLen > 0 && !plausible(drawn.String(), sp.text) {
		// Замене нет веры — остаётся нарисованное. Явно пустая замена
		// (rawLen == 0) сюда не попадает: так помечают украшения, которым
		// в тексте не место.
		for _, f := range sp.held {
			e.shown += len([]rune(f.text))
			e.frags = append(e.frags, f)
		}
		return
	}
	// Пробел по краям нарисованного замена обязана сохранить: капитель
	// размечают как «нарисовано "G ", читать "g"», и без этого правила
	// «MarketinG sidekick» выходит как «Marketingsidekick».
	if d := drawn.String(); strings.TrimSpace(sp.text) != "" {
		if strings.HasPrefix(d, " ") && !strings.HasPrefix(sp.text, " ") {
			sp.text = " " + sp.text
		}
		if strings.HasSuffix(d, " ") && !strings.HasSuffix(sp.text, " ") {
			sp.text += " "
		}
	}
	if sp.text == "" {
		return
	}
	w := sp.endX - sp.x
	if w < 0 {
		w = 0
	}
	e.shown += len([]rune(sp.text))
	e.frags = append(e.frags, frag{x: sp.x, y: sp.y, w: w, size: sp.size, text: sp.text})
}

// replacing возвращает номер самого наружного открытого блока с ActualText
// или -1, когда текст идёт как нарисован.
func (e *extractor) replacing() int {
	for i := range e.spans {
		if e.spans[i].actual {
			return i
		}
	}
	return -1
}

// noteShown запоминает положение нарисованного внутри заменяющего блока.
func (e *extractor) noteShown(i int, x, y, w, size float64, text string) {
	sp := &e.spans[i]
	if !sp.started {
		sp.started = true
		sp.x, sp.y, sp.size = x, y, size
		sp.endX = x
	}
	if end := x + w; end > sp.endX {
		sp.endX = end
	}
	sp.held = append(sp.held, frag{x: x, y: y, w: w, size: size, text: text})
}

// plausible решает, можно ли верить замене нарисованного drawn на actual.
//
// Два правила, оба выведены из сверки на 55 книгах 17.09.2026:
//
//  1. Букв и цифр в замене не меньше, чем нарисовано. «TEN» → «t» —
//     ошибка разметки (Apress), «ﬁ» → «fi» и «T» → «t» — правда.
//  2. Видимых знаков в замене тоже не меньше, чем нарисовано.
//  3. Замена из одних пробелов годится только для точек и пустоты: так
//     размечены пробел, нарисованный глифом-точкой, и точки-выноски
//     оглавления. В листингах тех же книг Apress пробельной заменой накрыт
//     отступ ВМЕСТЕ со скобкой («    {» → четыре неразрывных пробела),
//     и скобки из кода пропадали.
func plausible(drawn, actual string) bool {
	if strings.TrimSpace(actual) == "" {
		// Пробел, нарисованный глифом: знак на знак. Каким знаком глиф
		// читается по таблице шрифта — дело случая: в одной и той же книге
		// обычный пробел выходит точкой, а неразрывный — буквой «z»
		// («Когдаzархитектураzучитывается»).
		if len([]rune(drawn)) <= len([]rune(actual)) {
			return true
		}
		return strings.Trim(drawn, " .\u00a0\t") == ""
	}
	if wordRunes(actual) < wordRunes(drawn) {
		return false
	}
	// То же для знаков препинания: «(atpa):» → «ATPA» теряет скобку
	// и двоеточие, хотя букв столько же.
	return visibleRunes(actual) >= visibleRunes(drawn)
}

// visibleRunes считает всё, что видно на странице: не пробелы.
func visibleRunes(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case r >= '\ufb00' && r <= '\ufb06':
			n += 2
		case !unicode.IsSpace(r):
			n++
		}
	}
	return n
}

// wordRunes считает буквы и цифры: по ним сверяется правдоподобие замены.
// Лигатура считается за две буквы — столько в ней самое меньшее.
func wordRunes(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case r >= '\ufb00' && r <= '\ufb06':
			n += 2
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			n++
		}
	}
	return n
}

// spanProps достаёт словарь свойств из операндов BDC: он записан либо прямо
// в потоке, либо именем из раздела Properties ресурсов страницы.
func (e *extractor) spanProps(res Dict, operand Object) Dict {
	switch v := operand.(type) {
	case Dict:
		return v
	case Name:
		props := e.doc.dictOf(res["Properties"])
		return e.doc.dictOf(props[v])
	}
	return nil
}

// cleanActual убирает из замены то, чему в тексте не место. Управляющие знаки
// становятся пробелом: переводы строк разорвали бы строку, а знаками U+0007
// и U+0008 издательские конвейеры помечают отбивку и точки-выноски оглавления
// («Глава 1 ........ 15»), то есть именно пробел.
func cleanActual(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return ' '
		case (r >= '\u200b' && r <= '\u200d') || r == '\u2060' || r == '\ufeff':
			return -1 // невидимые знаки нулевой ширины тексту не нужны
		}
		return r
	}, s)
}
