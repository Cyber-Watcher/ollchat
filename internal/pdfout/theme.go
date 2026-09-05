package pdfout

// Вся геометрия документа собрана здесь одним местом: правки вида «сделать
// поля пошире» не должны требовать раскопок по рисовальщику.
//
// Единица измерения — типографский пункт (1/72 дюйма), как принято в PDF.

// theme — размеры страницы, кегли и отступы.
type theme struct {
	pageW, pageH float64 // размер страницы
	marginL      float64
	marginR      float64
	marginT      float64
	marginB      float64

	textSize float64 // основной текст
	textLead float64 // межстрочный интервал основного текста
	paraGap  float64 // отбивка между абзацами

	headSizes [6]float64 // кегли заголовков H1..H6
	headTop   [6]float64 // отбивка сверху
	headGap   float64    // отбивка снизу

	codeSize float64 // блок кода
	codeLead float64
	codePad  float64 // поля подложки блока кода

	quoteBar    float64 // толщина полосы цитаты
	quoteIndent float64 // отступ текста цитаты

	listIndent float64 // отступ на уровень вложенности
	listMarker float64 // ширина колонки метки пункта
	maxDepth   int     // дальше этого списки не сдвигаются

	tableSize    float64 // текст таблицы
	tableLead    float64
	tablePad     float64 // поля ячейки
	tableLine    float64 // толщина линий
	tableMinCol  float64 // ниже этого колонка не сжимается
	tableMaxFrac float64 // предел ширины одной колонки, доля страницы

	footSize float64 // номер страницы
}

// defaultTheme — оформление по умолчанию: A4 с полями около двух сантиметров.
func defaultTheme() theme {
	return theme{
		pageW: 595.28, pageH: 841.89, // A4 в пунктах
		marginL: 56, marginR: 56, marginT: 56, marginB: 52,

		textSize: 10.5,
		textLead: 14.7,
		paraGap:  7,

		// Заголовки: от крупного H1 до почти основного H6. Отбивка сверху
		// больше, чем снизу, — заголовок должен липнуть к своему тексту,
		// а не висеть посередине между кусками.
		headSizes: [6]float64{19, 16, 13.5, 12, 11, 10.5},
		headTop:   [6]float64{14, 12, 10, 9, 8, 8},
		headGap:   6,

		codeSize: 9,
		codeLead: 12,
		codePad:  5,

		quoteBar:    2,
		quoteIndent: 12,

		listIndent: 16,
		listMarker: 14,
		maxDepth:   6,

		tableSize:    9.5,
		tableLead:    12.5,
		tablePad:     4,
		tableLine:    0.4,
		tableMinCol:  46,
		tableMaxFrac: 0.6,

		footSize: 8,
	}
}

// contentWidth — ширина колонки текста.
func (t theme) contentWidth() float64 { return t.pageW - t.marginL - t.marginR }

// bottom — граница, ниже которой текст не печатается.
func (t theme) bottom() float64 { return t.pageH - t.marginB }

// headSize возвращает кегль заголовка уровня level (1..6).
func (t theme) headSize(level int) float64 {
	return t.headSizes[clampLevel(level)]
}

// headTopGap возвращает отбивку сверху для заголовка уровня level.
func (t theme) headTopGap(level int) float64 {
	return t.headTop[clampLevel(level)]
}

func clampLevel(level int) int {
	switch {
	case level < 1:
		return 0
	case level > 6:
		return 5
	default:
		return level - 1
	}
}

// indentFor — отступ списка на глубине depth.
//
// Глубже maxDepth отступ не растёт: иначе на седьмом уровне вложенности
// колонка текста схлопывается в несколько символов.
func (t theme) indentFor(depth int) float64 {
	if depth > t.maxDepth {
		depth = t.maxDepth
	}
	return float64(depth) * t.listIndent
}
