package main

import (
	"strings"
	"unicode/utf8"
)

// Таблицы с рамкой.
//
// Раньше колонки выравнивались через %-24s, и это разъезжалось на первом же
// длинном имени модели: спецификаторы ширины в Go считают байты, а не символы,
// поэтому кириллица в заголовках сдвигала всё вправо. Здесь ширина считается
// по рунам, а колонка не может оказаться уже своего содержимого.

type colAlign int

const (
	alignLeft colAlign = iota
	alignRight
)

type column struct {
	title string
	align colAlign
}

type table struct {
	cols []column
	rows [][]string
}

func newTable(cols ...column) *table {
	return &table{cols: cols}
}

// row добавляет строку. Лишние ячейки отбрасываются, недостающие дополняются
// пустыми — так одна опечатка в вызове не разваливает всю таблицу.
func (t *table) row(cells ...string) {
	out := make([]string, len(t.cols))
	copy(out, cells)
	t.rows = append(t.rows, out)
}

func (t *table) empty() bool { return len(t.rows) == 0 }

// widths — ширина каждой колонки по самому длинному значению, включая заголовок.
func (t *table) widths() []int {
	w := make([]int, len(t.cols))
	for i, c := range t.cols {
		w[i] = utf8.RuneCountInString(c.title)
	}
	for _, r := range t.rows {
		for i, cell := range r {
			if n := utf8.RuneCountInString(cell); n > w[i] {
				w[i] = n
			}
		}
	}
	return w
}

func pad(s string, width int, a colAlign) string {
	gap := width - utf8.RuneCountInString(s)
	if gap <= 0 {
		return s
	}
	if a == alignRight {
		return strings.Repeat(" ", gap) + s
	}
	return s + strings.Repeat(" ", gap)
}

// rule собирает горизонтальную линию: left ─── mid ─── right.
func (t *table) rule(w []int, left, mid, right string) string {
	parts := make([]string, len(w))
	for i, n := range w {
		parts[i] = strings.Repeat("─", n+2) // по пробелу с каждой стороны
	}
	return left + strings.Join(parts, mid) + right
}

func (t *table) line(cells []string, w []int) string {
	parts := make([]string, len(w))
	for i := range w {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		parts[i] = " " + pad(cell, w[i], t.cols[i].align) + " "
	}
	return "│" + strings.Join(parts, "│") + "│"
}

// String рисует таблицу целиком. Заголовки выравниваются так же, как их
// колонка: иначе числовой столбец с подписью слева читается вкривь.
func (t *table) String() string {
	w := t.widths()

	titles := make([]string, len(t.cols))
	for i, c := range t.cols {
		titles[i] = c.title
	}

	var b strings.Builder
	b.WriteString(t.rule(w, "┌", "┬", "┐") + "\n")
	b.WriteString(t.line(titles, w) + "\n")
	b.WriteString(t.rule(w, "├", "┼", "┤") + "\n")
	for _, r := range t.rows {
		b.WriteString(t.line(r, w) + "\n")
	}
	b.WriteString(t.rule(w, "└", "┴", "┘"))
	return b.String()
}
