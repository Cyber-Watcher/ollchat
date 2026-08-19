package pdfout

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/signintech/gopdf"
)

// Рисовальщик документа.
//
// Перенос по словам и разрывы страниц сделаны здесь вручную, а MultiCell
// из gopdf не используется вовсе: он не умеет переносить текст на новую
// страницу — просто обрывается, когда исчерпана высота прямоугольника.
// Зато Cell сдвигает текущий X ровно на ширину напечатанного, и это даёт
// главное, что нужно разметке: несколько начертаний в одной строке.

// token — слово вместе с начертанием и уже измеренной шириной.
type token struct {
	text  string
	style runStyle
	w     float64
	space float64 // ширина пробела после слова
	brk   bool    // после слова — жёсткий перенос строки
}

// wkey — ключ кеша измеренных ширин.
type wkey struct {
	text  string
	style runStyle
	size  float64
}

// painter — состояние вывода.
type painter struct {
	pdf     *gopdf.GoPdf
	th      theme
	y       float64 // верх следующей строки
	pages   int
	missing map[rune]bool
	wcache  map[wkey]float64

	// covers — какие символы умеет рисовать каждый зарегистрированный шрифт.
	// По ним выбирается резервный шрифт для знаков вроде ₽, которых
	// в Liberation нет.
	covers map[fontKey]map[rune]bool

	curFamily string
	curStyle  string
	curSize   float64
}

// newPainter готовит документ и первую страницу.
func newPainter(th theme, opt Options) (*painter, error) {
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{
		Unit:     gopdf.UnitPT,
		PageSize: gopdf.Rect{W: th.pageW, H: th.pageH},
	})

	missing := map[rune]bool{}
	covers, err := registerFonts(pdf, missing)
	if err != nil {
		return nil, err
	}

	title := opt.Meta.Title
	if title == "" {
		title = "Ответ модели"
	}
	pdf.SetInfo(gopdf.PdfInfo{
		Title:   title,
		Author:  opt.Meta.Model,
		Creator: "ollchat",
	})

	p := &painter{
		pdf: pdf, th: th, missing: missing,
		wcache: map[wkey]float64{},
		covers: covers,
	}
	p.newPage()
	return p, nil
}

// bytes собирает документ.
//
// Только GetBytesPdfReturnErr: GetBytesPdf при ошибке зовёт log.Fatalf
// и убивает процесс вместе с несохранённым разговором пользователя.
func (p *painter) bytes() ([]byte, error) { return p.pdf.GetBytesPdfReturnErr() }

// missingRunes возвращает отсортированный список символов без глифа.
func (p *painter) missingRunes() []rune {
	if len(p.missing) == 0 {
		return nil
	}
	out := make([]rune, 0, len(p.missing))
	for r := range p.missing {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// newPage начинает новую страницу и печатает её номер.
func (p *painter) newPage() {
	p.pdf.AddPage()
	p.pages++
	p.y = p.th.marginT
	p.footer()
}

// need обеспечивает h пунктов свободного места, при нехватке начиная страницу.
func (p *painter) need(h float64) bool {
	if p.y+h <= p.th.bottom() {
		return false
	}
	p.newPage()
	return true
}

// footer печатает номер страницы внизу.
func (p *painter) footer() {
	label := fmt.Sprintf("— %d —", p.pages)
	w := p.measure(label, styleDim, p.th.footSize)
	p.pdf.SetTextColor(140, 140, 140)
	p.drawText(label, styleDim, p.th.footSize, (p.th.pageW-w)/2, p.th.pageH-p.th.marginB+16)
	p.pdf.SetTextColor(0, 0, 0)
}

// setFont переключает шрифт под начертание.
//
// Лишние переключения стоят и времени, и места в потоке команд, поэтому
// текущее состояние запоминается.
func (p *painter) setFont(s runStyle, fb bool, size float64) {
	family, style := fontFor(s)
	if fb {
		family, style = fallbackFor(s)
	}
	if family == p.curFamily && style == p.curStyle && size == p.curSize {
		return
	}
	if err := p.pdf.SetFont(family, style, size); err != nil {
		// Шрифты зарегистрированы в newPainter, промахнуться тут можно
		// только ошибкой в коде — но валить документ из-за этого незачем.
		return
	}
	p.curFamily, p.curStyle, p.curSize = family, style, size
}

// measure меряет ширину текста тем начертанием, каким он будет напечатан.
//
// Мерить чужим шрифтом нельзя: строка, посчитанная обычным, а напечатанная
// жирным, вылезет за поле страницы.
func (p *painter) measure(s string, style runStyle, size float64) float64 {
	if s == "" {
		return 0
	}
	k := wkey{text: s, style: style, size: size}
	if w, ok := p.wcache[k]; ok {
		return w
	}

	// Строку приходится мерить по кускам: символ, которого нет в основном
	// шрифте, печатается резервным, а он другой ширины.
	var w float64
	for _, piece := range p.split(s, style) {
		w += p.measurePiece(piece.text, style, piece.fb, size)
	}
	p.wcache[k] = w
	return w
}

// measurePiece меряет кусок, целиком печатаемый одним шрифтом.
func (p *painter) measurePiece(s string, style runStyle, fb bool, size float64) float64 {
	p.setFont(style, fb, size)
	w, err := p.pdf.MeasureTextWidth(s)
	if err != nil {
		return float64(utf8.RuneCountInString(s)) * size * 0.5
	}
	return w
}

// drawText печатает строку начиная с точки (x, y) и возвращает её ширину.
//
// Единственное место, где текст попадает в документ: строка режется на куски
// по шрифтам, и каждый кусок печатается своим. Иначе знак рубля в середине
// слова заставил бы каждое место печати уметь то же самое.
func (p *painter) drawText(s string, style runStyle, size, x, y float64) float64 {
	cx := x
	for _, pc := range p.split(s, style) {
		p.setFont(style, pc.fb, size)
		// Положение задаём явно перед каждым куском: gopdf склеивает подряд
		// идущие ячейки с одинаковым шрифтом и Y, не сверяя X.
		p.pdf.SetXY(cx, y)
		_ = p.pdf.Cell(nil, pc.text)
		cx += p.measurePiece(pc.text, style, pc.fb, size)
	}
	return cx - x
}

// piece — кусок строки, печатаемый одним шрифтом.
type piece struct {
	text string
	fb   bool // рисуется резервным шрифтом
}

// split режет строку на куски по тому, какой шрифт умеет их нарисовать.
//
// Символ, которого нет ни в основном шрифте, ни в резервном (эмодзи, например),
// остаётся за основным: там его перехватит OnGlyphNotFound, нарисует □
// и внесёт в отчёт «символов без глифа». Молча терять символы нельзя.
func (p *painter) split(s string, style runStyle) []piece {
	mainFamily, mainStyle := fontFor(style)
	spareFamily, spareStyle := fallbackFor(style)
	main := p.covers[fontKey{mainFamily, mainStyle}]
	spare := p.covers[fontKey{spareFamily, spareStyle}]

	var out []piece
	var cur []rune
	curFB := false

	flush := func() {
		if len(cur) > 0 {
			out = append(out, piece{text: string(cur), fb: curFB})
			cur = cur[:0]
		}
	}

	for _, r := range s {
		fb := !main[r] && spare[r]
		if len(cur) > 0 && fb != curFB {
			flush()
		}
		curFB = fb
		cur = append(cur, r)
	}
	flush()
	return out
}

// tokenize режет поток кусков на слова с измеренной шириной.
func (p *painter) tokenize(runs []run, size float64) []token {
	var out []token
	for _, r := range runs {
		words := strings.Fields(r.text)
		trailing := strings.HasSuffix(r.text, " ")

		// Ведущий пробел куска принадлежит предыдущему слову: Fields его
		// отбрасывает, и без этого «**труба** между» слиплось бы
		// в «трубамежду» — пробел стоял перед куском, а не после.
		if leading := strings.HasPrefix(r.text, " "); leading && len(out) > 0 {
			if k := len(out) - 1; out[k].space == 0 && !out[k].brk {
				out[k].space = p.measure(" ", out[k].style, size)
			}
		}

		for i, w := range words {
			t := token{text: w, style: r.style, w: p.measure(w, r.style, size)}
			if i < len(words)-1 || trailing {
				t.space = p.measure(" ", r.style, size)
			}
			out = append(out, t)
		}
		if r.brk {
			if len(out) > 0 {
				out[len(out)-1].brk = true
				out[len(out)-1].space = 0
			} else {
				out = append(out, token{style: r.style, brk: true})
			}
		}
	}
	return out
}

// wrapTokens раскладывает слова по строкам жадно, слева направо.
func (p *painter) wrapTokens(tokens []token, width, size float64) [][]token {
	var (
		lines [][]token
		cur   []token
		x     float64
	)
	flush := func() {
		if len(cur) > 0 {
			lines = append(lines, cur)
			cur, x = nil, 0
		}
	}
	for _, t := range tokens {
		if t.text == "" && t.brk {
			flush()
			continue
		}
		// Слово шире всей колонки: режем его, иначе оно уедет за поле.
		if t.w > width {
			flush()
			for _, part := range p.splitLong(t.text, t.style, size, width) {
				lines = append(lines, []token{{
					text: part, style: t.style, w: p.measure(part, t.style, size),
				}})
			}
			continue
		}
		if x > 0 && x+t.w > width {
			flush()
		}
		cur = append(cur, t)
		x += t.w + t.space
		if t.brk {
			flush()
		}
	}
	flush()
	return lines
}

// splitLong режет слово, которое не помещается в колонку целиком.
//
// Такое приходит с длинными ссылками и склеенными идентификаторами.
// Обрезать молча нельзя — потеря текста незаметна и потому опасна.
func (p *painter) splitLong(s string, style runStyle, size, width float64) []string {
	var out []string
	runes := []rune(s)
	for len(runes) > 0 {
		// Двоичный поиск самого длинного куска, влезающего в ширину.
		lo, hi := 1, len(runes)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if p.measure(string(runes[:mid]), style, size) <= width {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		out = append(out, string(runes[:lo]))
		runes = runes[lo:]
	}
	return out
}

// drawLine печатает одну готовую строку, кусок за куском.
func (p *painter) drawLine(line []token, x, size, lead float64) {
	cx := x
	for _, t := range line {
		if t.text == "" {
			continue
		}
		if t.style&styleDim != 0 {
			p.pdf.SetTextColor(110, 110, 110)
		}
		p.drawText(t.text, t.style, size, cx, p.y)
		if t.style&styleDim != 0 {
			p.pdf.SetTextColor(0, 0, 0)
		}
		cx += t.w + t.space
	}
	p.y += lead
}

// writeFlow печатает поток кусков в колонке с переносом и разрывами страниц.
func (p *painter) writeFlow(runs []run, x, width, size, lead float64, quote int) {
	lines := p.wrapTokens(p.tokenize(runs, size), width, size)
	for _, line := range lines {
		// Разрыв проверяем перед каждой строкой, а не перед блоком: абзац
		// на сорок строк обязан переезжать через границу страницы.
		p.need(lead)
		if quote > 0 {
			p.quoteBar(x, lead, quote)
		}
		p.drawLine(line, x, size, lead)
	}
}

// quoteBar рисует полосу цитаты слева от строки.
//
// Полоса рисуется на каждую строку, а не одним отрезком на блок: тогда
// разрыв страницы посреди цитаты не требует ничего дополнительного.
func (p *painter) quoteBar(x, lead float64, depth int) {
	p.pdf.SetLineWidth(p.th.quoteBar)
	p.pdf.SetStrokeColor(180, 180, 180)
	for i := 0; i < depth; i++ {
		bx := x - p.th.quoteIndent + float64(i)*4
		p.pdf.Line(bx, p.y, bx, p.y+lead)
	}
	p.pdf.SetStrokeColor(0, 0, 0)
}

// blockX возвращает левую границу блока с учётом списков и цитат.
func (p *painter) blockX(b blockNode) (x, width float64) {
	x = p.th.marginL + p.th.indentFor(b.depth) + float64(b.quote)*p.th.quoteIndent
	width = p.th.pageW - p.th.marginR - x
	return x, width
}

// drawBlocks печатает все блоки подряд.
func (p *painter) drawBlocks(bs []blockNode) {
	for i, b := range bs {
		// Пункты списка стоят вплотную друг к другу, поэтому отбивку после
		// списка добавляет уже следующий блок: сам пункт не знает, последний
		// он или нет. Без этого цитата или таблица после списка прилипает
		// к последнему пункту — видно на глаз в готовом документе.
		if i > 0 && bs[i-1].kind == blkItem && b.kind != blkItem {
			p.y += p.th.paraGap
		}

		switch b.kind {
		case blkHeading:
			p.heading(b)
		case blkParagraph:
			p.paragraph(b)
		case blkItem:
			p.item(b)
		case blkCode:
			p.codeBlock(b)
		case blkRule:
			p.rule()
		case blkTable:
			x, width := p.blockX(b)
			p.drawTable(b.tbl, x, width)
			p.y += p.th.paraGap
		}
	}
}

// paragraph печатает абзац.
func (p *painter) paragraph(b blockNode) {
	x, width := p.blockX(b)
	p.writeFlow(b.runs, x, width, p.th.textSize, p.th.textLead, b.quote)
	p.y += p.th.paraGap
}

// heading печатает заголовок.
func (p *painter) heading(b blockNode) {
	size := p.th.headSize(b.level)
	lead := size * 1.35
	x, width := p.blockX(b)

	p.y += p.th.headTopGap(b.level)
	// Заголовок не должен оставаться внизу страницы в одиночестве:
	// требуем место под него и хотя бы одну строку текста под ним.
	p.need(lead + p.th.textLead)

	bold := make([]run, len(b.runs))
	for i, r := range b.runs {
		r.style |= styleBold
		bold[i] = r
	}
	p.writeFlow(bold, x, width, size, lead, b.quote)
	p.y += p.th.headGap
}

// item печатает пункт списка вместе с его меткой.
func (p *painter) item(b blockNode) {
	x, width := p.blockX(b)
	markerW := p.th.listMarker
	if b.marker != "" {
		if w := p.measure(b.marker+" ", 0, p.th.textSize); w > markerW {
			markerW = w
		}
	}

	if len(b.runs) == 0 {
		p.need(p.th.textLead)
		if b.marker != "" {
			p.drawText(b.marker, 0, p.th.textSize, x, p.y)
		}
		p.y += p.th.textLead
		return
	}

	lines := p.wrapTokens(p.tokenize(b.runs, p.th.textSize), width-markerW, p.th.textSize)
	for i, line := range lines {
		p.need(p.th.textLead)
		if b.quote > 0 {
			p.quoteBar(x, p.th.textLead, b.quote)
		}
		// Метка печатается только у первой строки пункта, продолжение
		// выравнивается по тексту, а не по метке.
		if i == 0 && b.marker != "" {
			p.drawText(b.marker, 0, p.th.textSize, x, p.y)
		}
		p.drawLine(line, x+markerW, p.th.textSize, p.th.textLead)
	}
}

// codeBlock печатает блок кода на серой подложке.
func (p *painter) codeBlock(b blockNode) {
	x, width := p.blockX(b)
	inner := width - 2*p.th.codePad

	p.y += p.th.paraGap / 2
	for _, srcLine := range b.code {
		// Табуляция в PDF не работает: разворачиваем сами, иначе отступы
		// в коде схлопнутся.
		line := strings.ReplaceAll(srcLine, "\t", "    ")

		parts := []string{line}
		if p.measure(line, styleCode, p.th.codeSize) > inner {
			parts = p.splitLong(line, styleCode, p.th.codeSize, inner)
		}
		for i, part := range parts {
			p.need(p.th.codeLead)
			p.pdf.SetFillColor(246, 246, 246)
			p.pdf.RectFromUpperLeftWithStyle(x, p.y-2, width, p.th.codeLead, "F")
			p.pdf.SetFillColor(0, 0, 0)

			text := part
			if i < len(parts)-1 {
				// Метка переноса: строка не обрезана, а продолжается ниже.
				text += string(wrapMark)
			}
			p.drawText(text, styleCode, p.th.codeSize, x+p.th.codePad, p.y)
			p.y += p.th.codeLead
		}
	}
	p.y += p.th.paraGap
}

// rule печатает горизонтальную линию.
func (p *painter) rule() {
	p.need(p.th.paraGap * 2)
	p.pdf.SetLineWidth(0.5)
	p.pdf.SetStrokeColor(200, 200, 200)
	y := p.y + p.th.paraGap/2
	p.pdf.Line(p.th.marginL, y, p.th.pageW-p.th.marginR, y)
	p.pdf.SetStrokeColor(0, 0, 0)
	p.y += p.th.paraGap * 2
}

// docHeader печатает шапку документа: заголовок, вопрос, модель и дату.
func (p *painter) docHeader(m Meta) {
	title := m.Title
	if title == "" {
		title = "Ответ модели"
	}
	p.writeFlow([]run{{text: title, style: styleBold}},
		p.th.marginL, p.th.contentWidth(), p.th.headSizes[0], p.th.headSizes[0]*1.35, 0)

	var sub []string
	if m.Model != "" {
		sub = append(sub, "модель: "+m.Model)
	}
	if !m.At.IsZero() {
		sub = append(sub, m.At.Format("2006.01.02 15:04"))
	}
	if len(sub) > 0 {
		p.writeFlow([]run{{text: strings.Join(sub, " · "), style: styleDim}},
			p.th.marginL, p.th.contentWidth(), p.th.textSize, p.th.textLead, 0)
	}

	if m.Question != "" {
		p.y += p.th.paraGap
		p.writeFlow([]run{{text: "Вопрос", style: styleBold}},
			p.th.marginL, p.th.contentWidth(), p.th.headSizes[2], p.th.headSizes[2]*1.35, 0)
		p.writeFlow([]run{{text: m.Question}},
			p.th.marginL+p.th.quoteIndent, p.th.contentWidth()-p.th.quoteIndent,
			p.th.textSize, p.th.textLead, 1)
		p.y += p.th.paraGap
		p.writeFlow([]run{{text: "Ответ", style: styleBold}},
			p.th.marginL, p.th.contentWidth(), p.th.headSizes[2], p.th.headSizes[2]*1.35, 0)
	}
	p.y += p.th.paraGap
}
