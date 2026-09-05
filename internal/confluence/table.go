package confluence

import (
	"regexp"
	"strings"
)

// Таблицы: главная трудность перевода.
//
// В таблицах Confluence живёт самое ценное — описания полей, коды, значения.
// Беда в том, что ячейка там не текст: внутри бывает вложенный объект
// на пять строк, список вариантов с пояснениями и ссылками, блок кода.
// Разведка на живой странице 25.08.2026: ячейка `Proxy` содержала объект
// с четырьмя полями и отступами, ячейка `TrueAPI → AuthType` — два варианта
// со ссылкой на стороннюю документацию.
//
// Markdown многострочные ячейки не держит. Решение: содержимое ячейки
// склеивается в одну строку, переносы заменяются на пробелы, вертикальная
// черта экранируется. Данные при этом целы — теряется только вёрстка,
// и это честный размен: таблица кодов должна доехать побайтово, а красота
// в репозитории вторична.

var reSpaces = regexp.MustCompile(`[ \t]+`)

// renderTable печатает таблицу markdown.
func renderTable(b *strings.Builder, n *node, c ctx) {
	rows := collectRows(n, c)
	if len(rows) == 0 {
		return
	}

	width := 0
	for _, r := range rows {
		if len(r) > width {
			width = len(r)
		}
	}

	b.WriteString("\n\n")
	head, body := rows[0], rows[1:]
	// Заголовок обязателен по синтаксису markdown. Если в первой строке
	// заголовков не было, ставится пустая шапка — иначе таблица не отрисуется
	// вовсе, а это хуже пустых заголовков.
	writeRow(b, head, width)
	b.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
	for _, r := range body {
		writeRow(b, r, width)
	}
	b.WriteString("\n")
}

func writeRow(b *strings.Builder, cells []string, width int) {
	b.WriteString("|")
	for i := 0; i < width; i++ {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		b.WriteString(" " + cell + " |")
	}
	b.WriteString("\n")
}

// collectRows собирает строки таблицы, разворачивая tbody и thead.
func collectRows(n *node, c ctx) [][]string {
	var rows [][]string
	var walk func(*node)
	walk = func(x *node) {
		for _, k := range x.kids {
			switch k.name {
			case "thead", "tbody", "tfoot":
				walk(k)
			case "tr":
				var cells []string
				for _, cell := range k.kids {
					if cell.name != "td" && cell.name != "th" {
						continue
					}
					cells = append(cells, cellText(cell, c))
				}
				if len(cells) > 0 {
					rows = append(rows, cells)
				}
			case "table":
				// Вложенная таблица: разворачиваем её строки в эту же.
				// Городить таблицу в таблице markdown всё равно не умеет.
				walk(k)
			}
		}
	}
	walk(n)
	return rows
}

// cellText переводит содержимое ячейки в одну строку markdown.
func cellText(cell *node, c ctx) string {
	inner := c
	inner.inCell = true
	inner.listDepth = 0

	var b strings.Builder
	for _, k := range cell.kids {
		render(&b, k, inner)
	}
	s := b.String()
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = reSpaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// tidy убирает следы перевода: тройные пустые строки, пробелы в конце строк.
// collapseSpaces схлопывает пробелы в строках и тройные пустые строки.
func collapseSpaces(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(reSpaces.ReplaceAllString(l, " "), " ")
	}
	s = strings.Join(lines, "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s) + "\n"
}
