package pdfout

import (
	"strings"
	"testing"
)

// makeTable собирает таблицу из простых строк для проверки арифметики ширин.
func makeTable(head []string, rows ...[]string) *tableData {
	t := &tableData{}
	for _, h := range head {
		t.head = append(t.head, cell{runs: []run{{text: h}}})
	}
	for _, r := range rows {
		var cells []cell
		for _, s := range r {
			cells = append(cells, cell{runs: []run{{text: s}}})
		}
		t.rows = append(t.rows, cells)
	}
	return t
}

func sum(xs []float64) float64 {
	total := 0.0
	for _, x := range xs {
		total += x
	}
	return total
}

// TestTableWidthsFitPage — таблица никогда не должна выпадать за колонку.
func TestTableWidthsFitPage(t *testing.T) {
	p := newTestPainter(t)
	width := p.th.contentWidth()

	cases := map[string]*tableData{
		"обычная": makeTable(
			[]string{"Параметр", "Значение", "Что делает"},
			[]string{"temperature", "0.6", "разброс ответа"},
			[]string{"top_k", "20", "ширина выбора"},
		),
		"широкие ячейки": makeTable(
			[]string{"Колонка", "Описание"},
			[]string{strings.Repeat("длинное значение ", 10), strings.Repeat("и ещё длиннее ", 12)},
		),
		"много колонок": makeTable(
			[]string{"a", "b", "c", "d", "e", "f", "g", "h"},
			[]string{"1", "2", "3", "4", "5", "6", "7", "8"},
		),
		"одна колонка": makeTable([]string{"одна"}, []string{"строка"}),
	}

	for name, tbl := range cases {
		widths, size := p.tableWidths(tbl, width)
		if got := sum(widths); got > width+0.5 {
			t.Errorf("%s: таблица шире колонки: %.1f при пределе %.1f", name, got, width)
		}
		if size <= 0 {
			t.Errorf("%s: неположительный кегль %.1f", name, size)
		}
		if len(widths) != columnCount(tbl) {
			t.Errorf("%s: колонок %d, ожидалось %d", name, len(widths), columnCount(tbl))
		}
		for i, w := range widths {
			if w <= 0 {
				t.Errorf("%s: колонка %d нулевой ширины", name, i)
			}
		}
	}
}

// TestTableWidthsUseWholeColumn: свободное место раздаётся, таблица
// не жмётся к левому краю.
func TestTableWidthsUseWholeColumn(t *testing.T) {
	p := newTestPainter(t)
	width := p.th.contentWidth()

	tbl := makeTable([]string{"a", "b"}, []string{"1", "2"})
	widths, _ := p.tableWidths(tbl, width)

	if got := sum(widths); got < width-1 {
		t.Errorf("узкая таблица не растянута: %.1f из %.1f", got, width)
	}
}

// TestShrinkRespectsMinimum: сжатие не должно схлопывать колонку в точку.
func TestShrinkRespectsMinimum(t *testing.T) {
	widths := []float64{300, 200, 100}
	const min = 46.0

	if !shrink(widths, 200, min) {
		t.Fatal("сжать на 200 пунктов было возможно, но не вышло")
	}
	for i, w := range widths {
		if w < min-0.01 {
			t.Errorf("колонка %d сжата ниже предела: %.1f при минимуме %.1f", i, w, min)
		}
	}
	if got := sum(widths); got > 400.5 {
		t.Errorf("сжали недостаточно: осталось %.1f вместо 400", got)
	}
}

// TestShrinkRefusesImpossible: если срезать столько нельзя, надо честно
// сказать «нет», а не портить таблицу.
func TestShrinkRefusesImpossible(t *testing.T) {
	widths := []float64{50, 50, 50}
	if shrink(widths, 200, 46) {
		t.Error("сжатие ниже предела выдало успех")
	}
}

// TestTableFontStepsDownWhenTooNarrow: когда колонки уже не сжать, кегль
// уменьшается — но не бесконечно.
func TestTableFontStepsDownWhenTooNarrow(t *testing.T) {
	p := newTestPainter(t)

	// Много колонок в узкой ширине: сжимать почти нечего.
	head := make([]string, 12)
	row := make([]string, 12)
	for i := range head {
		head[i] = "Колонка"
		row[i] = "значение"
	}
	tbl := makeTable(head, row)

	widths, size := p.tableWidths(tbl, 300)
	if got := sum(widths); got > 300.5 {
		t.Errorf("таблица не влезла: %.1f при пределе 300", got)
	}
	if size > p.th.tableSize {
		t.Errorf("кегль вырос вместо уменьшения: %.1f", size)
	}
	if size < 7 {
		t.Errorf("кегль уменьшен до нечитаемого: %.1f", size)
	}
}

// TestColumnCountTakesLongestRow: строка с лишней ячейкой не должна
// потерять её из-за короткой шапки.
func TestColumnCountTakesLongestRow(t *testing.T) {
	tbl := makeTable([]string{"a", "b"}, []string{"1", "2", "3"})
	if got := columnCount(tbl); got != 3 {
		t.Errorf("колонок насчитано %d, ожидалось 3", got)
	}
}

// TestRowHeightGrowsWithWrappedText: ячейка в несколько строк делает
// строку выше, иначе текст налезет на соседнюю.
func TestRowHeightGrowsWithWrappedText(t *testing.T) {
	p := newTestPainter(t)
	widths := []float64{80, 80}
	lead := p.th.tableLead

	short := []cell{{runs: []run{{text: "коротко"}}}, {runs: []run{{text: "тоже"}}}}
	long := []cell{
		{runs: []run{{text: strings.Repeat("длинный текст ячейки ", 5)}}},
		{runs: []run{{text: "тоже"}}},
	}

	hShort := p.rowHeight(short, widths, p.th.tableSize, lead)
	hLong := p.rowHeight(long, widths, p.th.tableSize, lead)

	if hLong <= hShort {
		t.Errorf("многострочная ячейка не увеличила высоту строки: %.1f против %.1f",
			hLong, hShort)
	}
}
