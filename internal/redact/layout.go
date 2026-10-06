package redact

import (
	"sort"
	"strings"
)

// Раскладка страницы по координатам слов: строки и таблицы.
//
// tesseract сам делит страницу на блоки, и таблица без линеек у него
// распадается на столбцы: название анализа, значение и норма попадают
// в разные абзацы, и .md выходил столбиками, где не понять, какое число
// к чему относится (образец 06.10.2026 — 39 листов с лабораторными
// таблицами). Поэтому строки страницы собираются заново по высоте слов,
// строка делится на клетки по широким промежуткам, а подряд идущие строки
// из нескольких клеток с общими столбцами становятся таблицей markdown.
// Абзацы обычного текста по-прежнему берутся у tesseract.

// Пороги — в долях средней высоты слова на странице: в точках они не годятся,
// кегль у документов разный.
const (
	cellGapK   = 1.5 // промежуток между словами шире — граница клетки
	rowGapK    = 4.0 // строки дальше друг от друга — таблица кончилась
	colTolK    = 2.5 // левые края клеток ближе — один столбец
	headingK   = 1.6 // строка выше средней во столько раз — заголовок
	headingMax = 10  // в заголовке не больше стольких слов

	tableMaxCols = 12

	junkConf    = 30  // слово неуверенней этого…
	junkHeightK = 0.5 // …и ниже такой доли строки — мусор, в .md не идёт
	junkRowConf = 20  // строка со средней уверенностью ниже — мусор целиком
)

// row — строка страницы: слова на одной высоте, разбитые на клетки.
type row struct {
	cells  [][]int // индексы слов по клеткам, слева направо
	y0, y1 float64 // средние верх и низ слов строки
	h      float64 // средняя высота слова
}

func (r row) all() []int {
	var out []int
	for _, c := range r.cells {
		out = append(out, c...)
	}
	return out
}

func medianHeight(words []Word, idx []int) float64 {
	hs := make([]float64, 0, len(idx))
	for _, i := range idx {
		if h := words[i].Box.Y1 - words[i].Box.Y0; h > 0 {
			hs = append(hs, h)
		}
	}
	if len(hs) == 0 {
		return 10
	}
	sort.Float64s(hs)
	return hs[len(hs)/2]
}

// pageRows собирает строки одной страницы сверху вниз. Слово идёт в строку,
// с полосой которой перекрывается хотя бы наполовину своей высоты; полоса —
// средняя по словам строки, а не их общий охват, иначе высокое слово
// склеило бы две соседние строки.
func pageRows(words []Word, idx []int, mh float64) []row {
	sorted := append([]int(nil), idx...)
	sort.SliceStable(sorted, func(a, b int) bool {
		ba, bb := words[sorted[a]].Box, words[sorted[b]].Box
		return ba.Y0+ba.Y1 < bb.Y0+bb.Y1
	})
	type acc struct {
		idx          []int
		sumY0, sumY1 float64
	}
	var rows []acc
	for _, i := range sorted {
		b := words[i].Box
		h := b.Y1 - b.Y0
		best, bestOv := -1, 0.0
		// Слова идут по высоте центра: строка-кандидат — среди последних.
		for j := len(rows) - 1; j >= 0 && j >= len(rows)-6; j-- {
			n := float64(len(rows[j].idx))
			y0, y1 := rows[j].sumY0/n, rows[j].sumY1/n
			ov := min(b.Y1, y1) - max(b.Y0, y0)
			if ov >= 0.5*min(h, y1-y0) && ov > bestOv {
				best, bestOv = j, ov
			}
		}
		if best < 0 {
			rows = append(rows, acc{})
			best = len(rows) - 1
		}
		rows[best].idx = append(rows[best].idx, i)
		rows[best].sumY0 += b.Y0
		rows[best].sumY1 += b.Y1
	}

	out := make([]row, 0, len(rows))
	for _, a := range rows {
		n := float64(len(a.idx))
		r := row{y0: a.sumY0 / n, y1: a.sumY1 / n}
		r.h = r.y1 - r.y0
		sort.SliceStable(a.idx, func(x, y int) bool { return words[a.idx[x]].Box.X0 < words[a.idx[y]].Box.X0 })
		gap := cellGapK * max(mh, r.h)
		var cell []int
		for k, i := range a.idx {
			if k > 0 && words[i].Box.X0-words[a.idx[k-1]].Box.X1 > gap {
				r.cells = append(r.cells, cell)
				cell = nil
			}
			cell = append(cell, i)
		}
		r.cells = append(r.cells, cell)
		out = append(out, r)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].y0 < out[b].y0 })
	return out
}

// tableGrid раскладывает строки по столбцам: столбец — группа близких левых
// краёв клеток. nil — это не таблица: столбцов меньше двух или больше
// tableMaxCols, или меньше двух строк, где клетки легли в разные столбцы.
// По столбцам раскладываются слова, а не клетки: флаг «F» у края стоит
// к названию анализа то дальше, то ближе порога клетки, и клетка «F PROTEIN,
// TOTAL» иначе легла бы в столбец флагов, оставив столбец названий пустым.
// Слова строки в одном столбце склеиваются.
func tableGrid(words []Word, rows []row, mh float64) [][][]int {
	tol := colTolK * mh
	var xs []float64
	for _, r := range rows {
		for _, c := range r.cells {
			xs = append(xs, words[c[0]].Box.X0)
		}
	}
	sort.Float64s(xs)
	var starts []float64
	for _, x := range xs {
		if len(starts) == 0 || x-starts[len(starts)-1] > tol {
			starts = append(starts, x)
		}
	}
	if len(starts) < 2 || len(starts) > tableMaxCols {
		return nil
	}
	grid := make([][][]int, len(rows))
	multi := 0
	for ri, r := range rows {
		grid[ri] = make([][]int, len(starts))
		used := 0
		for _, i := range r.all() {
			x := words[i].Box.X0
			k := sort.Search(len(starts), func(k int) bool { return starts[k] > x }) - 1
			if k < 0 {
				k = 0
			}
			if len(grid[ri][k]) == 0 {
				used++
			}
			grid[ri][k] = append(grid[ri][k], i)
		}
		if used >= 2 {
			multi++
		}
	}
	if multi < 2 {
		return nil
	}
	return grid
}

// emptyText — в строке нет ничего, кроме знаков препинания и мусора
// распознавания.
func emptyText(t string) bool { return strings.Trim(t, " ,.;:-/|_'`‘’\"") == "" }

// tableMD печатает таблицу markdown. Строки, ставшие пустыми (всё
// персональное убрано), и пустые столбцы выпадают. Если остался один
// столбец, таблица не нужна — вторым значением идут строки.
func tableMD(grid [][][]int, render func([]int) string) (lines []string, table bool) {
	if len(grid) == 0 {
		return nil, false
	}
	ncol := len(grid[0])
	var cells [][]string
	for _, r := range grid {
		out := make([]string, ncol)
		empty := true
		for k, c := range r {
			t := strings.TrimSpace(render(c))
			if emptyText(t) {
				t = ""
			}
			out[k] = strings.ReplaceAll(t, "|", `\|`)
			if out[k] != "" {
				empty = false
			}
		}
		if !empty {
			cells = append(cells, out)
		}
	}
	var keep []int
	for k := 0; k < ncol; k++ {
		for _, r := range cells {
			if r[k] != "" {
				keep = append(keep, k)
				break
			}
		}
	}
	if len(keep) < 2 {
		for _, r := range cells {
			for _, k := range keep {
				lines = append(lines, r[k])
			}
		}
		return lines, false
	}
	line := func(r []string) string {
		parts := make([]string, len(keep))
		for i, k := range keep {
			parts[i] = r[k]
		}
		return "| " + strings.Join(parts, " | ") + " |"
	}
	lines = append(lines, line(cells[0]), "|"+strings.Repeat(" --- |", len(keep)))
	for _, r := range cells[1:] {
		lines = append(lines, line(r))
	}
	return lines, true
}

// pageMD — текст одной страницы для .md: абзацы, заголовки, строки-поля
// и таблицы в том порядке, в каком они стоят на листе.
func pageMD(words []Word, idx []int, render func([]int) string) []string {
	if len(idx) == 0 {
		return nil
	}
	mh := medianHeight(words, idx)
	// Мусор распознавания: неуверенные «слова» ниже половины строки — пунктир
	// линеек и точки грязи. В образце 06.10.2026 таких 1159 из 12344 слов
	// («Cmm», «kAR», «FH» с уверенностью 0–33 и высотой 0–5 pt при медиане
	// 7 pt); в .md они давали строки бессмыслицы поперёк таблиц.
	var kept []int
	for _, i := range idx {
		if w := words[i]; w.Conf >= junkConf || w.Box.Y1-w.Box.Y0 >= junkHeightK*mh {
			kept = append(kept, i)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	// Строка, где tesseract не уверен почти ни в чём, — пунктир рамки таблицы
	// («““cnnu““nunun-unu…», «A A A A R SRR»: средняя уверенность 4–16 на листе
	// 15 образца), а не текст.
	var rows []row
	for _, r := range pageRows(words, kept, mh) {
		sum, n := 0.0, 0
		for _, i := range r.all() {
			sum += words[i].Conf
			n++
		}
		if sum/float64(n) >= junkRowConf {
			rows = append(rows, r)
		}
	}

	var md, buf []string
	var bufKey [3]int
	lastField := false
	blank := func() {
		if len(md) > 0 && md[len(md)-1] != "" {
			md = append(md, "")
		}
	}
	flush := func() {
		if len(buf) > 0 {
			blank()
			md = append(md, strings.Join(buf, " "))
			buf = nil
			lastField = false
		}
	}
	// field — отдельная строка с жёстким переносом: поле бланка или клетка
	// строки, которая не сложилась в таблицу.
	field := func(t string) {
		flush()
		if !lastField {
			blank()
		}
		md = append(md, t+"  ")
		lastField = true
	}
	text := func(r row) {
		ws := r.all()
		t := render(ws)
		w := words[ws[0]]
		if key := [3]int{w.Page, w.Block, w.Par}; key != bufKey {
			flush()
			bufKey = key
		}
		switch {
		case emptyText(t):
		case headingRe.MatchString(t) || (r.h >= headingK*mh && len(ws) <= headingMax):
			flush()
			blank()
			md = append(md, "### "+strings.TrimSuffix(t, ":"))
			blank()
			lastField = false
		case labelRe.MatchString(t) && labelRe.FindStringIndex(t)[0] == 0:
			// Строка-поле («Exam: CT …») — отдельной строкой, а не в абзац.
			field(t)
		default:
			buf = append(buf, t)
		}
	}

	// Соседние строки — одна таблица, только если у них хотя бы два общих
	// левых края клеток. Иначе шапка бланка (телефоны слева, «FINAL RESULT»
	// справа), строка мусора от пунктира и поля «Order Date | Received»
	// слипались в одну таблицу на восемь столбцов, и в PDF клетки рвались
	// по буквам (образец 06.10.2026, листы 6–11).
	aligned := func(a, b row) bool {
		n := 0
		for _, ca := range a.cells {
			for _, cb := range b.cells {
				if d := words[ca[0]].Box.X0 - words[cb[0]].Box.X0; d <= colTolK*mh && d >= -colTolK*mh {
					n++
					break
				}
			}
		}
		return n >= 2
	}
	for i := 0; i < len(rows); {
		if len(rows[i].cells) >= 2 {
			j := i + 1
			for j < len(rows) && len(rows[j].cells) >= 2 && rows[j].y0-rows[j-1].y1 <= rowGapK*mh &&
				aligned(rows[j-1], rows[j]) {
				j++
			}
			if grid := tableGrid(words, rows[i:j], mh); j-i >= 2 && grid != nil {
				lines, table := tableMD(grid, render)
				if table {
					flush()
					blank()
					md = append(md, lines...)
					blank()
					lastField = false
				} else {
					for _, l := range lines {
						field(l)
					}
				}
				i = j
				continue
			}
			for _, c := range rows[i].cells {
				if t := render(c); !emptyText(t) {
					field(t)
				}
			}
			i++
			continue
		}
		text(rows[i])
		i++
	}
	flush()
	return md
}
