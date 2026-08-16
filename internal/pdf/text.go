package pdf

import (
	"fmt"
	"math"
	"strings"
)

// matrix — матрица преобразования PDF [a b c d e f].
type matrix struct{ a, b, c, dd, e, f float64 }

var identity = matrix{1, 0, 0, 1, 0, 0}

// mul возвращает произведение m × n.
func (m matrix) mul(n matrix) matrix {
	return matrix{
		a:  m.a*n.a + m.b*n.c,
		b:  m.a*n.b + m.b*n.dd,
		c:  m.c*n.a + m.dd*n.c,
		dd: m.c*n.b + m.dd*n.dd,
		e:  m.e*n.a + m.f*n.c + n.e,
		f:  m.e*n.b + m.f*n.dd + n.f,
	}
}

// extractor превращает содержимое страницы в текст.
//
// Он не выводит текст по ходу разбора, а собирает куски с их положением:
// строки и столбцы восстанавливаются потом, в layout(). Иначе таблицы
// рассыпаются по ячейкам — каждая ячейка нарисована своим блоком BT…ET.
type extractor struct {
	doc      *Document
	unit     int // номер разбираемой страницы: он входит в метку рисунка
	frags    []frag
	fonts    map[int]*font
	images   []pageImage // картинки страницы в порядке отрисовки
	unmapped int
	shown    int
	depth    int
}

// pageImage — картинка, нарисованная на странице. Порядок отрисовки важен:
// по нему картинки нумеруются и в тексте, и при выгрузке, иначе метка
// «рисунок 44.2» указывала бы на разные вещи в двух местах.
type pageImage struct {
	obj    int // номер объекта, по нему отсеиваются повторы
	stream *Stream
	w, h   int // размер самой картинки в точках
}

func newExtractor(d *Document) *extractor {
	return &extractor{doc: d, fonts: map[int]*font{}}
}

// page извлекает текст одной страницы.
func (e *extractor) page(page Dict) string {
	e.frags = e.frags[:0]
	e.images = e.images[:0]
	content := e.doc.contentOf(page)
	res, _ := e.doc.Resolve(page["Resources"]).(Dict)
	e.run(content, res)
	return cleanText(layout(e.frags))
}

// state — состояние текста внутри BT…ET.
type state struct {
	tm, tlm  matrix
	fontSize float64
	leading  float64
	hscale   float64
	charSp   float64
	wordSp   float64
	cur      *font
}

func (e *extractor) run(content []byte, res Dict) {
	if e.depth > 8 {
		return
	}
	p := newParser(content, e.doc)
	var operands []Object
	var gstack []matrix
	ctm := identity
	st := state{tm: identity, tlm: identity, hscale: 1}

	// pos возвращает текущее положение пера и кегль в единицах страницы.
	pos := func() (float64, float64, float64) {
		trm := st.tm.mul(ctm)
		scale := math.Hypot(trm.a, trm.b)
		if scale == 0 {
			scale = 1
		}
		return trm.e, trm.f, st.fontSize * scale
	}

	// advance сдвигает перо на ширину показанного, как это делает отрисовщик.
	// Без сдвига положение следующего куска считается неверно и строка
	// рассыпается на буквы.
	advance := func(tx float64) {
		st.tm = matrix{1, 0, 0, 1, tx, 0}.mul(st.tm)
	}

	show := func(s String) {
		sh := st.cur.decode(s)
		e.unmapped += sh.unmapped
		x, y, size := pos()
		tx := (sh.width/1000*st.fontSize +
			float64(sh.glyphs)*st.charSp +
			float64(sh.spaces)*st.wordSp) * st.hscale
		if sh.text != "" {
			e.shown += len([]rune(sh.text))
			scale := math.Hypot(st.tm.mul(ctm).a, st.tm.mul(ctm).b)
			// Координаты берутся из файла и у повреждённого документа бывают
			// NaN или бесконечностью. Дальше по ним считается раскладка, где
			// такое значение превращается в переполнение, поэтому отсеиваем
			// сразу: кусок без внятного положения всё равно некуда поставить.
			if w := tx * scale; finite(x) && finite(y) && finite(w) && finite(size) {
				e.frags = append(e.frags, frag{x: x, y: y, w: w, size: size, text: sh.text})
			}
		}
		advance(tx)
	}

	num := func(i int) float64 {
		if i < 0 || i >= len(operands) {
			return 0
		}
		v, _ := toFloat(operands[i])
		return v
	}

	for {
		obj, op, isOp, err := p.token()
		if err == errEOF {
			return
		}
		if err != nil {
			operands = operands[:0]
			continue
		}
		if !isOp {
			if len(operands) < 64 {
				operands = append(operands, obj)
			}
			continue
		}

		switch op {
		case "q":
			gstack = append(gstack, ctm)
		case "Q":
			if n := len(gstack); n > 0 {
				ctm = gstack[n-1]
				gstack = gstack[:n-1]
			}
		case "cm":
			if len(operands) >= 6 {
				ctm = matrix{num(0), num(1), num(2), num(3), num(4), num(5)}.mul(ctm)
			}
		case "BT":
			st.tm, st.tlm = identity, identity
		case "ET":
		case "Tf":
			if len(operands) >= 2 {
				if name, ok := operands[0].(Name); ok {
					st.cur = e.font(res, name)
				}
				st.fontSize = num(1)
			}
		case "TL":
			st.leading = num(0)
		case "Tc":
			st.charSp = num(0)
		case "Tw":
			st.wordSp = num(0)
		case "Tz":
			if v := num(0); v != 0 {
				st.hscale = v / 100
			}
		case "Td":
			if len(operands) >= 2 {
				st.tlm = matrix{1, 0, 0, 1, num(0), num(1)}.mul(st.tlm)
				st.tm = st.tlm
			}
		case "TD":
			if len(operands) >= 2 {
				st.leading = -num(1)
				st.tlm = matrix{1, 0, 0, 1, num(0), num(1)}.mul(st.tlm)
				st.tm = st.tlm
			}
		case "Tm":
			if len(operands) >= 6 {
				st.tlm = matrix{num(0), num(1), num(2), num(3), num(4), num(5)}
				st.tm = st.tlm
			}
		case "T*":
			st.tlm = matrix{1, 0, 0, 1, 0, -st.leading}.mul(st.tlm)
			st.tm = st.tlm
		case "Tj":
			if len(operands) >= 1 {
				if s, ok := operands[len(operands)-1].(String); ok {
					show(s)
				}
			}
		case "'", "\"":
			if op == "\"" && len(operands) >= 3 {
				st.wordSp, st.charSp = num(0), num(1)
			}
			st.tlm = matrix{1, 0, 0, 1, 0, -st.leading}.mul(st.tlm)
			st.tm = st.tlm
			if len(operands) >= 1 {
				if s, ok := operands[len(operands)-1].(String); ok {
					show(s)
				}
			}
		case "TJ":
			if len(operands) >= 1 {
				arr, _ := operands[len(operands)-1].(Array)
				for _, item := range arr {
					switch v := item.(type) {
					case String:
						show(v)
					default:
						f, ok := toFloat(v)
						if !ok {
							continue
						}
						advance(-f / 1000 * st.fontSize * st.hscale)
					}
				}
			}
		case "Do":
			if len(operands) >= 1 {
				if name, ok := operands[0].(Name); ok {
					x, y, _ := pos()
					e.xobject(res, name, ctm, x, y)
				}
			}
		case "BI":
			// Встроенное изображение: пропускаем до EI, иначе двоичные данные
			// разберутся как мусорные операторы.
			skipInlineImage(p)
		}
		operands = operands[:0]
	}
}

// finite сообщает, что число пригодно для расчёта положения на странице.
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// font находит шрифт по имени в ресурсах и кэширует разбор.
func (e *extractor) font(res Dict, name Name) *font {
	fonts, _ := e.doc.Resolve(res["Font"]).(Dict)
	if fonts == nil {
		return nil
	}
	entry := fonts[name]
	if ref, ok := entry.(Ref); ok {
		if f, ok := e.fonts[ref.Num]; ok {
			return f
		}
		f := e.doc.loadFont(e.doc.dictOf(entry))
		e.fonts[ref.Num] = f
		return f
	}
	return e.doc.loadFont(e.doc.dictOf(entry))
}

// xobject разбирает внешний объект: форма разбирается как содержимое, картинка
// запоминается и отмечается в тексте — модель должна знать, что на странице
// есть рисунок, даже когда сам рисунок ей не отправляют.
func (e *extractor) xobject(res Dict, name Name, ctm matrix, penX, penY float64) {
	xobjs, _ := e.doc.Resolve(res["XObject"]).(Dict)
	if xobjs == nil {
		return
	}
	s, ok := e.doc.Resolve(xobjs[name]).(*Stream)
	if !ok {
		return
	}
	switch st, _ := e.doc.Resolve(s.Dict["Subtype"]).(Name); st {
	case "Image":
		num := 0
		if ref, ok := xobjs[name].(Ref); ok {
			num = ref.Num
		}
		w, _ := toInt(e.doc.Resolve(s.Dict["Width"]))
		h, _ := toInt(e.doc.Resolve(s.Dict["Height"]))
		e.images = append(e.images, pageImage{obj: num, stream: s, w: w, h: h})
		e.markImage(len(e.images), ctm, w, h)
		return
	case "Form":
	default:
		return
	}
	data, err := e.doc.Decode(s)
	if err != nil && len(data) == 0 {
		return
	}
	sub, _ := e.doc.Resolve(s.Dict["Resources"]).(Dict)
	if sub == nil {
		sub = res
	}
	e.depth++
	e.run(data, sub)
	e.depth--
}

// markImage ставит метку рисунка в том месте страницы, где он нарисован.
// Единица квадрата картинки растягивается матрицей, поэтому её размер и
// положение читаются прямо из ctm.
func (e *extractor) markImage(idx int, ctm matrix, w, h int) {
	width := math.Hypot(ctm.a, ctm.b)
	height := math.Hypot(ctm.c, ctm.dd)
	if width < 24 || height < 24 {
		return // подложки, линейки и прочая мелочь только засоряют текст
	}
	// Метка обязана совпадать с тем, чем рисунок потом просят показать:
	// «страница.номер». Раньше в метке стоял только номер внутри страницы,
	// и модель, увидев «[рисунок 1]» на четвёртой странице, просила рисунок 1
	// и получала картинку с первой. Найдено на живом документе.
	e.frags = append(e.frags, frag{
		x:    ctm.e,
		y:    ctm.f + height, // метка ставится по верхнему краю рисунка
		w:    width,
		size: 10,
		text: fmt.Sprintf("[рисунок %d.%d: %d×%d]", e.unit, idx, w, h),
	})
}

// skipInlineImage перематывает содержимое за встроенное изображение.
func skipInlineImage(p *parser) {
	for p.pos+1 < len(p.b) {
		if p.b[p.pos] == 'E' && p.b[p.pos+1] == 'I' &&
			(p.pos == 0 || isSpace(p.b[p.pos-1])) &&
			(p.pos+2 >= len(p.b) || !isRegular(p.b[p.pos+2])) {
			p.pos += 2
			return
		}
		p.pos++
	}
	p.pos = len(p.b)
}

// cleanText приводит извлечённое к опрятному виду. Внутренние пробелы не
// схлопываются: именно они держат столбцы таблиц. Убираются только хвостовые
// пробелы, лишние пустые строки и общий отступ всей страницы.
func cleanText(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.ReplaceAll(line, "\u00a0", " ")
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, line)
	}
	return strings.Trim(stripCommonIndent(out), "\n")
}

// stripCommonIndent убирает отступ, общий для всех строк: поля страницы смысла
// не несут, а место в контексте занимают.
func stripCommonIndent(lines []string) string {
	indent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, " "))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	if indent <= 0 {
		return strings.Join(lines, "\n")
	}
	for i, line := range lines {
		if len(line) >= indent {
			lines[i] = line[indent:]
		}
	}
	return strings.Join(lines, "\n")
}
