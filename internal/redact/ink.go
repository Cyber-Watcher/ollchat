package redact

import (
	"image"
	"image/color"
	"strings"

	"golang.org/x/image/draw"
)

// Чернила вне распознанного текста: почерк, подпись, печать, логотип.
//
// tesseract такие места часто не видит вовсе. На образце 03.10.2026 из
// четырёх строк рукописной пометки он вернул слова только для первой, а
// инициалы и дата под ней остались бы видны: ни одно правило по тексту
// не замажет то, чего в тексте нет. Поэтому смотрим на сами пиксели: тёмное,
// что не покрыто уверенно прочитанным словом, — подозрительно.
const (
	inkDPI       = 150
	inkDark      = 110  // серое темнее — чернила; маркер-выделитель светлее и не в счёт
	inkCell      = 12   // клетка сетки, точек картинки при inkDPI
	inkMinPx     = 150  // меньше — точка, запятая, соринка скана
	inkMinH      = 5.0  // pt; ниже — линейка или подчёркивание
	inkReach     = 2    // клетки через сколько пустых считаются соседними: штрихи почерка рвутся
	inkPrinted   = 70.0 // средняя уверенность строки, начиная с которой она печатная
	inkRuleLen   = 75   // точек при inkDPI (полдюйма): прямой штрих длиннее — линейка, не чернила
	inkRuleGap   = 2    // разрыв линейки в точках, который ещё не делит её на две
	inkRuleThick = 6    // точек при inkDPI (~1 мм): толще — уже не линейка, а пятно
)

// oddShape — «слово», которое по рамке не похоже на буквы: на знак приходится
// больше inkGlyphWide высот строки. tesseract читает росчерк подписи как
// слово («\Ш», уверенность 64; «№», 47 в уверенной строке), и такое слово
// закрывало подпись от поиска чернил (синтетический набор 03.10.2026: две
// подписи из восьми). У печатного слова на знак — около половины высоты.
const inkGlyphWide = 1.5

func oddShape(w Word) bool {
	n := len([]rune(strings.TrimSpace(w.Text)))
	h := w.Box.Y1 - w.Box.Y0
	if n == 0 || h <= 0 {
		return true
	}
	return (w.Box.X1-w.Box.X0)/(float64(n)*h) > inkGlyphWide
}

// eraseRules стирает из карты чернил прямые линии: линейки и рамки таблиц,
// подчёркивания, полосы бланка. Без этого сетка таблицы связывала все свои
// клетки в одну область «чернил вне текста», и таблица уходила под чёрное
// целиком вместе с прочитанным в ней (синтетический набор 03.10.2026: обе
// таблицы направления на анализы, 4 строки из 43 нужных пропали из .md).
// Почерк, подпись, печать и логотип прямых линий в полдюйма не дают.
//
// Линия — длинная И тонкая: точка стирается, когда вдоль одной оси тёмное
// тянется на inkRuleLen, а поперёк — не толще inkRuleThick. Плотное пятно
// (жирная подпись, залитый логотип) длинное по обеим осям и остаётся.
// Длины считаются по нетронутой карте: стёртая горизонталь иначе рвала бы
// вертикаль, проходящую через ту же точку.
func eraseRules(dark []bool, w, h int) {
	// runs — длина тёмного отрезка через каждую точку вдоль оси; gap —
	// какой разрыв ещё не делит отрезок.
	runs := func(n, m int, at func(line, i int) int, gap int) []int {
		out := make([]int, len(dark))
		for line := 0; line < m; line++ {
			start, last := -1, -1
			flush := func() {
				for i := start; start >= 0 && i <= last; i++ {
					out[at(line, i)] = last - start + 1
				}
			}
			for i := 0; i < n; i++ {
				if !dark[at(line, i)] {
					continue
				}
				if start < 0 || i-last > gap+1 {
					flush()
					start = i
				}
				last = i
			}
			flush()
		}
		return out
	}
	row := func(y, x int) int { return y*w + x }
	col := func(x, y int) int { return y*w + x }
	// Толщина считается с тем же допуском на разрывы: штрихованное или рваное
	// пятно (почерк, печать) иначе выглядело бы тонким поперёк.
	hLen, vLen := runs(w, h, row, inkRuleGap), runs(h, w, col, inkRuleGap)
	for i := range dark {
		if dark[i] && ((hLen[i] >= inkRuleLen && vLen[i] <= inkRuleThick) ||
			(vLen[i] >= inkRuleLen && hLen[i] <= inkRuleThick)) {
			dark[i] = false
		}
	}
}

func inkRegions(p Page, page int, words []Word) []Region {
	img, k := scaleGray(p, inkDPI)
	b := img.Bounds()
	dark := make([]bool, b.Dx()*b.Dy())
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dark[y*b.Dx()+x] = img.Pix[y*img.Stride+x] < inkDark
		}
	}
	eraseRules(dark, b.Dx(), b.Dy())

	// Печатное слово с низкой уверенностью («OLP=166.60» вместо «DLP=166.60»)
	// узнаётся по строке: у печатной строки средняя уверенность высокая,
	// у рукописной — низкая.
	sum, cnt := map[[4]int]float64{}, map[[4]int]int{}
	for _, w := range words {
		if w.Page == page {
			key := [4]int{w.Page, w.Block, w.Par, w.Line}
			sum[key] += w.Conf
			cnt[key]++
		}
	}
	for _, w := range words {
		if w.Page != page {
			continue
		}
		key := [4]int{w.Page, w.Block, w.Par, w.Line}
		printed := !oddShape(w) && (w.Conf >= 60 || (w.Conf >= 40 && sum[key]/float64(cnt[key]) >= inkPrinted))
		if !printed && w.Kind == KindNone {
			continue
		}
		x0, y0 := max(0, int((w.Box.X0-1)*k)), max(0, int((w.Box.Y0-1)*k))
		x1, y1 := min(b.Dx(), int((w.Box.X1+1)*k)+1), min(b.Dy(), int((w.Box.Y1+1)*k)+1)
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				dark[y*b.Dx()+x] = false
			}
		}
	}

	gw, gh := b.Dx()/inkCell, b.Dy()/inkCell
	occ := make([]bool, gw*gh)
	for cy := 0; cy < gh; cy++ {
		for cx := 0; cx < gw; cx++ {
			n := 0
			for y := cy * inkCell; y < (cy+1)*inkCell; y++ {
				for x := cx * inkCell; x < (cx+1)*inkCell; x++ {
					if dark[y*b.Dx()+x] {
						n++
					}
				}
			}
			occ[cy*gw+cx] = n > inkCell // хотя бы строка чернил в клетке
		}
	}

	var out []Region
	seen := make([]bool, len(occ))
	for start := range occ {
		if !occ[start] || seen[start] {
			continue
		}
		seen[start] = true
		stack := []int{start}
		minX, minY, maxX, maxY := gw, gh, -1, -1
		for len(stack) > 0 {
			c := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			cx, cy := c%gw, c/gw
			minX, minY, maxX, maxY = min(minX, cx), min(minY, cy), max(maxX, cx), max(maxY, cy)
			for ny := max(0, cy-inkReach); ny <= min(gh-1, cy+inkReach); ny++ {
				for nx := max(0, cx-inkReach); nx <= min(gw-1, cx+inkReach); nx++ {
					if n := ny*gw + nx; occ[n] && !seen[n] {
						seen[n] = true
						stack = append(stack, n)
					}
				}
			}
		}
		// Точная рамка — по самим чернилам внутри клеток.
		px0, py0, px1, py1 := b.Dx(), b.Dy(), -1, -1
		total := 0
		for y := minY * inkCell; y < (maxY+1)*inkCell; y++ {
			for x := minX * inkCell; x < (maxX+1)*inkCell; x++ {
				if dark[y*b.Dx()+x] {
					total++
					px0, py0, px1, py1 = min(px0, x), min(py0, y), max(px1, x), max(py1, y)
				}
			}
		}
		if total < inkMinPx {
			continue
		}
		box := Rect{float64(px0) / k, float64(py0) / k, float64(px1+1) / k, float64(py1+1) / k}
		if box.Y1-box.Y0 < inkMinH {
			continue
		}
		out = append(out, Region{Page: page, Box: box})
	}
	return out
}

// pad — поле вокруг замазанного, pt: рамка tesseract прилегает к буквам
// вплотную, и края букв иначе остаются видны.
const pad = 1.5

// boxes — что замазать на странице: подряд идущие скрытые слова одной строки
// одним прямоугольником (промежутки между словами тоже ничего не должны
// выдавать) и области чернил.
func boxes(words []Word, ink []Region, page int) []Rect {
	var out []Rect
	var cur *Rect
	var curLine [4]int
	for _, w := range words {
		if w.Page != page {
			continue
		}
		key := [4]int{w.Page, w.Block, w.Par, w.Line}
		if w.Kind != KindNone && cur != nil && key == curLine {
			cur.X0, cur.Y0 = min(cur.X0, w.Box.X0), min(cur.Y0, w.Box.Y0)
			cur.X1, cur.Y1 = max(cur.X1, w.Box.X1), max(cur.Y1, w.Box.Y1)
			continue
		}
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
		if w.Kind != KindNone {
			r := w.Box
			cur, curLine = &r, key
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	for _, r := range ink {
		if r.Page == page {
			out = append(out, r.Box)
		}
	}
	return out
}

// paint рисует чёрное поверх картинки страницы в её исходном разрешении.
// Возвращает новую картинку; серая остаётся серой — так страница в PDF
// не вырастет втрое.
func paint(p Page, page int, words []Word, ink []Region) image.Image {
	b := p.Image.Bounds()
	var dst draw.Image
	if g, ok := p.Image.(*image.Gray); ok {
		// Построчно: у серой картинки из JPEG строка выровнена до кратного 8
		// (Stride больше ширины), и копия Pix целиком сдвигала каждую строку.
		c := image.NewGray(b)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			copy(c.Pix[c.PixOffset(b.Min.X, y):][:b.Dx()], g.Pix[g.PixOffset(b.Min.X, y):][:b.Dx()])
		}
		dst = c
	} else {
		c := image.NewRGBA(b)
		draw.Draw(c, b, p.Image, b.Min, draw.Src)
		dst = c
	}
	kx, ky := float64(b.Dx())/p.Width, float64(b.Dy())/p.Height
	black := image.NewUniform(color.Black)
	for _, r := range boxes(words, ink, page) {
		rr := image.Rect(
			b.Min.X+int((r.X0-pad)*kx), b.Min.Y+int((r.Y0-pad)*ky),
			b.Min.X+int((r.X1+pad)*kx)+1, b.Min.Y+int((r.Y1+pad)*ky)+1,
		).Intersect(b)
		draw.Draw(dst, rr, black, image.Point{}, draw.Src)
	}
	return dst
}
