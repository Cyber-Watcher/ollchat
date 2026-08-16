package epub

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

// Превращение главы XHTML в текст.
//
// Структуру здесь задаёт разметка, а не координаты, как в PDF: абзац —
// это <p>, строка таблицы — <tr>, заголовок — <h1>. Поэтому разбор простой,
// но требует терпимости: живые книги полны незакрытых тегов и сущностей,
// которых нет в XML, и строгий разбор на них останавливается.

// htmlDoc — итог разбора главы.
type htmlDoc struct {
	title string
	text  string
	imgs  []htmlImage
}

// htmlImage — картинка, на которую ссылается глава.
type htmlImage struct {
	href string // путь относительно самой главы
	alt  string
}

// blockTags — теги, после которых начинается новая строка.
var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"blockquote": true, "section": true, "article": true, "header": true,
	"footer": true, "figure": true, "figcaption": true, "hr": true,
	"table": true, "thead": true, "tbody": true, "ul": true, "ol": true,
	"dt": true, "dd": true, "pre": true, "aside": true, "nav": true,
}

// skipTags — содержимое этих тегов в текст не идёт.
var skipTags = map[string]bool{"head": true, "script": true, "style": true, "title": true}

// headingTags — заголовки отбиваются пустой строкой с обеих сторон.
var headingTags = map[string]bool{"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true}

// parseHTML разбирает главу: собирает текст, заголовок и ссылки на картинки.
func parseHTML(data []byte, section int) htmlDoc {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity

	var (
		out      strings.Builder
		doc      htmlDoc
		skip     int  // глубина внутри пропускаемого тега
		pre      int  // глубина внутри <pre>: пробелы там значимы
		inHead   bool // <title> берём только из <head>
		inTitle  bool //
		heading  int  // глубина внутри заголовка
		cell     bool // была ли уже ячейка в текущей строке таблицы
		titleBuf strings.Builder
	)

	newline := func(blank bool) {
		s := out.String()
		if s == "" {
			return
		}
		if blank {
			if !strings.HasSuffix(s, "\n\n") {
				if strings.HasSuffix(s, "\n") {
					out.WriteString("\n")
				} else {
					out.WriteString("\n\n")
				}
			}
			return
		}
		if !strings.HasSuffix(s, "\n") {
			out.WriteString("\n")
		}
	}

	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			switch name {
			case "head":
				inHead = true
			case "title":
				if inHead {
					inTitle = true
				}
			}
			if skipTags[name] {
				skip++
				continue
			}
			if skip > 0 {
				continue
			}
			switch {
			case name == "img" || name == "image":
				href := attr(t, "src")
				if href == "" {
					href = attr(t, "href") // <image xlink:href=…> внутри SVG
				}
				if href != "" {
					doc.imgs = append(doc.imgs, htmlImage{href: href, alt: attr(t, "alt")})
					label := fmt.Sprintf("[рисунок %d.%d", section, len(doc.imgs))
					if alt := strings.TrimSpace(attr(t, "alt")); alt != "" {
						label += ": " + alt
					}
					newline(false)
					out.WriteString(label + "]")
					newline(false)
				}
			case name == "li":
				newline(false)
				out.WriteString("• ")
			case name == "td" || name == "th":
				if cell {
					out.WriteString(" | ")
				}
				cell = true
			case name == "tr":
				newline(false)
				cell = false
			case name == "pre":
				pre++
				newline(true)
			case headingTags[name]:
				heading++
				newline(true)
			case blockTags[name]:
				newline(false)
			}

		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			switch name {
			case "head":
				inHead = false
			case "title":
				if inTitle {
					doc.title = strings.TrimSpace(titleBuf.String())
					inTitle = false
				}
			}
			if skipTags[name] {
				if skip > 0 {
					skip--
				}
				continue
			}
			if skip > 0 {
				continue
			}
			switch {
			case name == "pre":
				if pre > 0 {
					pre--
				}
				newline(true)
			case headingTags[name]:
				if heading > 0 {
					heading--
				}
				newline(true)
			case name == "p" || name == "div" || name == "blockquote" || name == "table":
				newline(true)
			case blockTags[name]:
				newline(false)
			}

		case xml.CharData:
			if inTitle {
				titleBuf.Write(t)
				continue
			}
			if skip > 0 {
				continue
			}
			if pre > 0 {
				out.Write(t)
				continue
			}
			writeInline(&out, string(t))
		}
	}

	doc.text = tidy(out.String())
	if doc.title == "" {
		doc.title = firstHeading(doc.text)
	}
	return doc
}

// writeInline добавляет текст, схлопывая пробелы: в разметке переводы строк
// расставлены для удобства чтения исходника и смысла не несут.
func writeInline(out *strings.Builder, s string) {
	if strings.TrimSpace(s) == "" {
		// Пробел между тегами всё же разделяет слова.
		if s != "" && !endsWithSpace(out.String()) {
			out.WriteByte(' ')
		}
		return
	}
	leading := s[0] == ' ' || s[0] == '\n' || s[0] == '\t' || s[0] == '\r'
	trailing := strings.HasSuffix(s, " ") || strings.HasSuffix(s, "\n") ||
		strings.HasSuffix(s, "\t") || strings.HasSuffix(s, "\r")

	if leading && !endsWithSpace(out.String()) {
		out.WriteByte(' ')
	}
	out.WriteString(strings.Join(strings.Fields(s), " "))
	if trailing {
		out.WriteByte(' ')
	}
}

func endsWithSpace(s string) bool {
	if s == "" {
		return true
	}
	switch s[len(s)-1] {
	case ' ', '\n', '\t':
		return true
	}
	return false
}

// attr достаёт значение атрибута без учёта регистра и пространства имён.
func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if strings.EqualFold(a.Name.Local, name) {
			return strings.TrimSpace(a.Value)
		}
	}
	return ""
}

// tidy убирает хвостовые пробелы и лишние пустые строки.
func tidy(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, " ", " "), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t\r")
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
	return strings.Trim(strings.Join(out, "\n"), "\n ")
}

// firstHeading берёт первую непустую строку как заголовок раздела: у книг,
// собранных из FB2, заголовка в <head> обычно нет.
func firstHeading(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[рисунок") {
			continue
		}
		if r := []rune(line); len(r) > 80 {
			return string(r[:80]) + "…"
		}
		return line
	}
	return ""
}

// navLink — ссылка из оглавления EPUB 3.
type navLink struct{ href, text string }

// navLinks собирает ссылки из файла навигации: разбирать его как строгий XML
// не выйдет, потому что структура вложенных списков у книг разная.
func navLinks(data []byte) []navLink {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity

	var out []navLink
	var cur *navLink
	var buf strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if strings.EqualFold(t.Name.Local, "a") {
				cur = &navLink{href: attr(t, "href")}
				buf.Reset()
			}
		case xml.CharData:
			if cur != nil {
				buf.Write(t)
			}
		case xml.EndElement:
			if strings.EqualFold(t.Name.Local, "a") && cur != nil {
				cur.text = strings.Join(strings.Fields(buf.String()), " ")
				if cur.href != "" && cur.text != "" {
					out = append(out, *cur)
				}
				cur = nil
			}
		}
	}
	return out
}
