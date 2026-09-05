package pdfout

import (
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Разбор markdown в плоскую модель документа.
//
// Парсер тот же, что рисует ленту на экране (goldmark с набором GFM внутри
// glamour), и набор расширений взят такой же намеренно: документ обязан
// показывать то же, что пользователь видел в ответе. Разойдись они —
// и сохранённый PDF отличался бы от прочитанного на экране.

// run — кусок строки одного начертания.
type run struct {
	text  string
	style runStyle
	brk   bool // после куска — жёсткий перенос строки
}

// blockKind — вид блока документа.
type blockKind int

const (
	blkParagraph blockKind = iota
	blkHeading
	blkItem // пункт списка
	blkCode
	blkRule
	blkTable
)

// blockNode — один блок будущего документа.
//
// Список плоский, а вложенность выражена полями depth и quote. Рисовальщику
// дерево не нужно: разрывы страниц по плоской последовательности считаются
// проще, чем по дереву, и без рекурсии.
type blockNode struct {
	kind   blockKind
	level  int    // уровень заголовка, 1..6
	depth  int    // вложенность списка, 0 — верхний уровень
	quote  int    // глубина цитирования, 0 — не цитата
	marker string // готовая метка пункта: «•» или «1.»
	runs   []run
	code   []string // строки блока кода, как есть
	lang   string
	tbl    *tableData
}

// cell — ячейка таблицы.
type cell struct{ runs []run }

// tableData — разобранная таблица.
type tableData struct {
	head  []cell
	rows  [][]cell
	align []east.Alignment
}

// walkState — то, что нужно знать при обходе, но чего нет в самом узле.
type walkState struct {
	depth int // вложенность списка
	quote int // вложенность цитат
}

// parseMarkdown разбирает текст в плоский список блоков.
func parseMarkdown(src string) []blockNode {
	if strings.TrimSpace(src) == "" {
		return nil
	}
	source := []byte(src)
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	doc := md.Parser().Parse(text.NewReader(source))

	var out []blockNode
	walkBlocks(doc, source, walkState{}, &out)
	return out
}

// walkBlocks обходит дерево вручную, а не через ast.Walk.
//
// Обходу нужен контекст — глубина списка, глубина цитаты, номер пункта, —
// а в сигнатуру ast.Walker его не передать, пришлось бы держать состояние
// снаружи и угадывать, когда его сбрасывать.
func walkBlocks(n ast.Node, src []byte, st walkState, out *[]blockNode) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {

		case *ast.Heading:
			*out = append(*out, blockNode{
				kind: blkHeading, level: node.Level, quote: st.quote,
				runs: inlineRuns(node, src, 0),
			})

		case *ast.Paragraph, *ast.TextBlock:
			// TextBlock появляется внутри «плотных» пунктов списка вместо
			// абзаца — для нас это одно и то же.
			runs := inlineRuns(c, src, 0)
			if len(runs) > 0 {
				*out = append(*out, blockNode{
					kind: blkParagraph, depth: st.depth, quote: st.quote, runs: runs,
				})
			}

		case *ast.List:
			walkList(node, src, st, out)

		case *ast.Blockquote:
			inner := st
			inner.quote++
			walkBlocks(node, src, inner, out)

		case *ast.FencedCodeBlock:
			*out = append(*out, blockNode{
				kind: blkCode, depth: st.depth, quote: st.quote,
				code: linesOf(node, src), lang: string(node.Language(src)),
			})

		case *ast.CodeBlock:
			*out = append(*out, blockNode{
				kind: blkCode, depth: st.depth, quote: st.quote,
				code: linesOf(node, src),
			})

		case *ast.HTMLBlock:
			// Сырой HTML показываем как код. Молча выбрасывать нельзя:
			// в ответе модели это чаще всего осмысленный текст.
			if lines := linesOf(node, src); len(lines) > 0 {
				*out = append(*out, blockNode{
					kind: blkCode, depth: st.depth, quote: st.quote, code: lines,
				})
			}

		case *ast.ThematicBreak:
			*out = append(*out, blockNode{kind: blkRule, quote: st.quote})

		case *east.Table:
			if t := parseTable(node, src); t != nil {
				*out = append(*out, blockNode{kind: blkTable, quote: st.quote, tbl: t})
			}

		default:
			// Незнакомый контейнер обходим внутрь: лучше показать содержимое
			// без оформления, чем потерять его.
			if c.Type() == ast.TypeBlock {
				walkBlocks(c, src, st, out)
			}
		}
	}
}

// walkList раскладывает список в пункты, сохраняя нумерацию и вложенность.
func walkList(l *ast.List, src []byte, st walkState, out *[]blockNode) {
	num := l.Start
	if num == 0 {
		num = 1
	}
	for item := l.FirstChild(); item != nil; item = item.NextSibling() {
		marker := listMarker(l, num, st.depth)
		num++

		// Первый абзац пункта несёт метку, остальное содержимое — вложенное.
		first := true
		inner := st
		inner.depth = st.depth + 1

		for c := item.FirstChild(); c != nil; c = c.NextSibling() {
			switch child := c.(type) {
			case *ast.Paragraph, *ast.TextBlock:
				runs := inlineRuns(c, src, 0)
				if len(runs) == 0 {
					continue
				}
				b := blockNode{
					kind: blkItem, depth: st.depth, quote: st.quote, runs: runs,
				}
				if first {
					b.marker = marker
					first = false
				}
				*out = append(*out, b)
			case *ast.List:
				walkList(child, src, inner, out)
			default:
				walkBlocks(item, src, inner, out)
				c = nil // содержимое пункта уже обошли целиком
			}
			if c == nil {
				break
			}
		}

		// Пункт без текста (например, только вложенный список) всё равно
		// должен показать свою метку.
		if first && marker != "" {
			*out = append(*out, blockNode{
				kind: blkItem, depth: st.depth, quote: st.quote, marker: marker,
			})
		}
	}
}

// listMarker подбирает метку пункта.
func listMarker(l *ast.List, index, depth int) string {
	if l.IsOrdered() {
		return strconv.Itoa(index) + "."
	}
	switch depth % 3 {
	case 0:
		return "•"
	case 1:
		return "◦"
	default:
		return "–"
	}
}

// linesOf собирает строки блока дословно, вместе с отступами.
func linesOf(n ast.Node, src []byte) []string {
	lines := n.Lines()
	out := make([]string, 0, lines.Len())
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		out = append(out, strings.TrimRight(string(seg.Value(src)), "\r\n"))
	}
	// Пустые строки в конце блока кода только съедают место на странице.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}

// inlineRuns собирает куски строки вместе с их начертаниями.
func inlineRuns(n ast.Node, src []byte, base runStyle) []run {
	var out []run
	add := func(s string, style runStyle, brk bool) {
		if s == "" && !brk {
			return
		}
		// Соседние куски одного начертания склеиваем: меньше кусков —
		// меньше вызовов рисования и точнее измерение ширины.
		if k := len(out) - 1; k >= 0 && out[k].style == style && !out[k].brk {
			out[k].text += s
			out[k].brk = brk
			return
		}
		out = append(out, run{text: s, style: style, brk: brk})
	}

	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {

		case *ast.Text:
			s := string(node.Value(src))
			switch {
			case node.HardLineBreak():
				add(s, base, true)
			case node.SoftLineBreak():
				// Мягкий перенос в markdown — обычный пробел: строку
				// заново разложит наш собственный перенос по словам.
				add(s+" ", base, false)
			default:
				add(s, base, false)
			}

		case *ast.String:
			add(string(node.Value), base, false)

		case *ast.CodeSpan:
			add(inlineText(node, src), base|styleCode, false)

		case *ast.Emphasis:
			style := base | styleItalic
			if node.Level >= 2 {
				style = base | styleBold
			}
			out = append(out, inlineRuns(node, src, style)...)

		case *ast.Link:
			out = append(out, inlineRuns(node, src, base)...)
			if dest := string(node.Destination); dest != "" {
				add(" ("+dest+")", base|styleDim, false)
			}

		case *ast.AutoLink:
			add(string(node.URL(src)), base|styleDim, false)

		case *ast.Image:
			alt := inlineText(node, src)
			if alt == "" {
				alt = "рисунок"
			}
			add("["+alt+": "+string(node.Destination)+"]", base|styleDim, false)

		case *east.Strikethrough:
			// Зачёркивание в PDF не рисуем — печатаем текст как есть,
			// чтобы содержимое не пропало.
			out = append(out, inlineRuns(node, src, base)...)

		case *ast.RawHTML:
			add(rawHTMLText(node, src), base|styleCode, false)

		default:
			if c.Type() == ast.TypeInline {
				out = append(out, inlineRuns(c, src, base)...)
			}
		}
	}
	return out
}

// inlineText собирает голый текст узла без оформления.
func inlineText(n ast.Node, src []byte) string {
	var sb strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {
		case *ast.Text:
			sb.Write(node.Value(src))
		case *ast.String:
			sb.Write(node.Value)
		default:
			sb.WriteString(inlineText(c, src))
		}
	}
	return sb.String()
}

// rawHTMLText собирает содержимое сырого HTML-фрагмента.
func rawHTMLText(n *ast.RawHTML, src []byte) string {
	var sb strings.Builder
	for i := 0; i < n.Segments.Len(); i++ {
		seg := n.Segments.At(i)
		sb.Write(seg.Value(src))
	}
	return sb.String()
}

// parseTable переносит таблицу GFM в модель документа.
func parseTable(t *east.Table, src []byte) *tableData {
	out := &tableData{align: t.Alignments}
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		cells := make([]cell, 0, 4)
		for c := row.FirstChild(); c != nil; c = c.NextSibling() {
			cells = append(cells, cell{runs: inlineRuns(c, src, 0)})
		}
		if _, ok := row.(*east.TableHeader); ok {
			out.head = cells
			continue
		}
		out.rows = append(out.rows, cells)
	}
	if len(out.head) == 0 && len(out.rows) == 0 {
		return nil
	}
	return out
}
