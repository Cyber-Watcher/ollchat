package pdfout

import (
	east "github.com/yuin/goldmark/extension/ast"
)

// Таблицы markdown в PDF.
//
// Ответы модели полны таблиц, и терять их обидно, поэтому таблица никогда
// не выпадает за поле страницы: сначала срезаются самые широкие колонки,
// потом уменьшается кегль, и только в крайнем случае текст в ячейках
// переносится на несколько строк.

// drawTable печатает таблицу целиком.
func (p *painter) drawTable(t *tableData, x, width float64) {
	if t == nil {
		return
	}
	widths, size := p.tableWidths(t, width)
	lead := p.th.tableLead * (size / p.th.tableSize)

	p.y += p.th.paraGap / 2
	if len(t.head) > 0 {
		p.drawRow(t.head, widths, t.align, x, size, lead, true)
	}
	for _, row := range t.rows {
		// Строка не должна разрываться пополам, поэтому место проверяется
		// на всю её высоту сразу.
		h := p.rowHeight(row, widths, size, lead)
		if p.need(h) && len(t.head) > 0 {
			// Перенесли строку на новую страницу — шапка обязана поехать
			// следом, иначе колонки не опознать.
			p.drawRow(t.head, widths, t.align, x, size, lead, true)
		}
		p.drawRow(row, widths, t.align, x, size, lead, false)
	}
}

// tableWidths подбирает ширины колонок и кегль.
func (p *painter) tableWidths(t *tableData, width float64) ([]float64, float64) {
	n := columnCount(t)
	if n == 0 {
		return nil, p.th.tableSize
	}

	size := p.th.tableSize
	for attempt := 0; ; attempt++ {
		widths := p.naturalWidths(t, n, width, size)

		total := 0.0
		for _, w := range widths {
			total += w
		}
		if total <= width {
			// Свободное место раздаём поровну — таблица во всю колонку
			// смотрится лучше жмущейся к левому краю.
			if extra := (width - total) / float64(n); extra > 0 {
				for i := range widths {
					widths[i] += extra
				}
			}
			return widths, size
		}

		// Срезаем лишнее с самых широких колонок, не опускаясь ниже предела.
		if shrink(widths, total-width, p.th.tableMinCol) {
			return widths, size
		}

		// Не помогло — уменьшаем кегль ступенькой и пробуем снова.
		if attempt >= 2 || size <= 7.5 {
			// Дальше уменьшать некуда: раздаём поровну, текст перенесётся
			// внутри ячеек на несколько строк.
			even := width / float64(n)
			for i := range widths {
				widths[i] = even
			}
			return widths, size
		}
		size -= 1
	}
}

// naturalWidths считает желаемые ширины колонок по самой длинной ячейке.
func (p *painter) naturalWidths(t *tableData, n int, width, size float64) []float64 {
	widths := make([]float64, n)
	maxCol := width * p.th.tableMaxFrac

	measureRow := func(cells []cell, bold bool) {
		for i, c := range cells {
			if i >= n {
				break
			}
			w := p.cellNaturalWidth(c, size, bold) + 2*p.th.tablePad
			if w > maxCol {
				w = maxCol
			}
			if w > widths[i] {
				widths[i] = w
			}
		}
	}
	measureRow(t.head, true)
	for _, row := range t.rows {
		measureRow(row, false)
	}
	for i := range widths {
		if widths[i] < p.th.tableMinCol {
			widths[i] = p.th.tableMinCol
		}
	}
	return widths
}

// cellNaturalWidth — ширина ячейки в одну строку.
func (p *painter) cellNaturalWidth(c cell, size float64, bold bool) float64 {
	total := 0.0
	for _, r := range c.runs {
		style := r.style
		if bold {
			style |= styleBold
		}
		total += p.measure(r.text, style, size)
	}
	return total
}

// shrink срезает need пунктов с самых широких колонок.
// Возвращает false, если срезать столько, не опускаясь ниже min, не вышло.
func shrink(widths []float64, need, min float64) bool {
	for need > 0.5 {
		// Ищем самую широкую колонку, которую ещё можно сжать.
		best, bestW := -1, min
		for i, w := range widths {
			if w > bestW {
				best, bestW = i, w
			}
		}
		if best < 0 {
			return false
		}
		// Сжимаем до второй по ширине, чтобы срезать равномерно.
		second := min
		for i, w := range widths {
			if i != best && w > second {
				second = w
			}
		}
		step := widths[best] - second
		if step <= 0.5 {
			step = widths[best] - min
		}
		if step > need {
			step = need
		}
		if step <= 0 {
			return false
		}
		widths[best] -= step
		need -= step
	}
	return true
}

// columnCount — число колонок таблицы.
func columnCount(t *tableData) int {
	n := len(t.head)
	for _, row := range t.rows {
		if len(row) > n {
			n = len(row)
		}
	}
	return n
}

// cellLines раскладывает содержимое ячейки по строкам её колонки.
func (p *painter) cellLines(c cell, width, size float64, bold bool) [][]token {
	runs := c.runs
	if bold {
		runs = make([]run, len(c.runs))
		for i, r := range c.runs {
			r.style |= styleBold
			runs[i] = r
		}
	}
	inner := width - 2*p.th.tablePad
	if inner < 8 {
		inner = 8
	}
	return p.wrapTokens(p.tokenize(runs, size), inner, size)
}

// rowHeight считает высоту строки по самой высокой ячейке.
func (p *painter) rowHeight(cells []cell, widths []float64, size, lead float64) float64 {
	max := 1
	for i, c := range cells {
		if i >= len(widths) {
			break
		}
		if n := len(p.cellLines(c, widths[i], size, false)); n > max {
			max = n
		}
	}
	return float64(max)*lead + p.th.tablePad
}

// drawRow печатает одну строку таблицы с рамкой и выравниванием.
func (p *painter) drawRow(cells []cell, widths []float64, align []east.Alignment, x, size, lead float64, head bool) {
	lines := make([][][]token, len(widths))
	rows := 1
	for i := range widths {
		var c cell
		if i < len(cells) {
			c = cells[i]
		}
		lines[i] = p.cellLines(c, widths[i], size, head)
		if n := len(lines[i]); n > rows {
			rows = n
		}
	}
	h := float64(rows)*lead + p.th.tablePad

	p.need(h)
	top := p.y

	if head {
		total := 0.0
		for _, w := range widths {
			total += w
		}
		p.pdf.SetFillColor(238, 238, 238)
		p.pdf.RectFromUpperLeftWithStyle(x, top, total, h, "F")
		p.pdf.SetFillColor(0, 0, 0)
	}

	cx := x
	for i, w := range widths {
		p.y = top + p.th.tablePad/2
		for _, line := range lines[i] {
			lineW := 0.0
			for _, t := range line {
				lineW += t.w + t.space
			}
			// Выравнивание задаётся самой таблицей в markdown.
			off := p.th.tablePad
			switch alignOf(align, i) {
			case east.AlignRight:
				off = w - p.th.tablePad - lineW
			case east.AlignCenter:
				off = (w - lineW) / 2
			}
			if off < p.th.tablePad {
				off = p.th.tablePad
			}
			p.drawLine(line, cx+off, size, lead)
		}
		cx += w
	}
	p.y = top + h

	p.tableBorder(x, top, widths, h)
}

// tableBorder рисует рамку строки и разделители колонок.
func (p *painter) tableBorder(x, top float64, widths []float64, h float64) {
	total := 0.0
	for _, w := range widths {
		total += w
	}
	p.pdf.SetLineWidth(p.th.tableLine)
	p.pdf.SetStrokeColor(200, 200, 200)

	p.pdf.Line(x, top, x+total, top)
	p.pdf.Line(x, top+h, x+total, top+h)

	cx := x
	p.pdf.Line(cx, top, cx, top+h)
	for _, w := range widths {
		cx += w
		p.pdf.Line(cx, top, cx, top+h)
	}
	p.pdf.SetStrokeColor(0, 0, 0)
}

// alignOf возвращает выравнивание колонки i.
func alignOf(align []east.Alignment, i int) east.Alignment {
	if i < len(align) {
		return align[i]
	}
	return east.AlignNone
}
