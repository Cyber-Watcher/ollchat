package pdf

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// ErrNoText возвращается, когда текстового слоя в документе нет: обычно это
// скан, где страницы — картинки. Такой документ нужно распознавать, а не читать.
var ErrNoText = errors.New("в документе нет текстового слоя (похоже на скан)")

// ErrGarbledText возвращается, когда текст есть, но шрифты не дают сопоставить
// коды символам: без таблицы /ToUnicode получится мусор, и лучше сказать прямо.
var ErrGarbledText = errors.New("текстовый слой нечитаем: в шрифтах нет таблицы соответствия Unicode")

// Options задаёт, какую часть документа извлекать.
type Options struct {
	FirstPage int // с какой страницы начать, начиная с 1
	MaxPages  int // сколько страниц взять; 0 — все
}

// Page — текст одной страницы.
type Page struct {
	Number int
	Text   string
}

// Result — итог извлечения.
type Result struct {
	Pages      []Page // только запрошенные страницы
	TotalPages int    // сколько всего страниц в документе
	Title      string
	Author     string
	Truncated  bool // страниц больше, чем показано
}

// Text собирает текст всех извлечённых страниц с заголовками страниц.
func (r *Result) Text() string {
	var b strings.Builder
	for i, p := range r.Pages {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "── страница %d ──\n", p.Number)
		if p.Text == "" {
			b.WriteString("(пусто)")
			continue
		}
		b.WriteString(p.Text)
	}
	return b.String()
}

// ExtractFile читает документ с диска и извлекает текст.
func ExtractFile(path string, opt Options) (*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Extract(data, opt)
}

// Extract извлекает текст из документа в памяти.
func Extract(data []byte, opt Options) (res *Result, err error) {
	// Повреждённый файл не должен ронять приложение — см. recover.go.
	defer catch("разбор PDF", &err)

	doc, err := Open(data)
	if err != nil {
		return nil, err
	}
	pages := doc.Pages()
	if len(pages) == 0 {
		return nil, errors.New("в документе не найдено ни одной страницы")
	}

	first := opt.FirstPage
	if first < 1 {
		first = 1
	}
	if first > len(pages) {
		return nil, fmt.Errorf("в документе %d страниц, запрошено начало со страницы %d", len(pages), first)
	}
	last := len(pages)
	if opt.MaxPages > 0 && first-1+opt.MaxPages < last {
		last = first - 1 + opt.MaxPages
	}

	res = &Result{
		TotalPages: len(pages),
		Truncated:  last < len(pages),
		Title:      doc.infoString("Title"),
		Author:     doc.infoString("Author"),
	}
	ex := newExtractor(doc)
	for i := first - 1; i < last; i++ {
		ex.unit = i + 1
		res.Pages = append(res.Pages, Page{Number: i + 1, Text: ex.page(pages[i])})
	}

	if ex.shown == 0 {
		if ex.unmapped > 0 {
			return nil, ErrGarbledText
		}
		return nil, ErrNoText
	}
	// Если сопоставить не удалось большую часть кодов, текст бесполезен.
	if ex.unmapped > ex.shown {
		return nil, ErrGarbledText
	}
	return res, nil
}

// infoString достаёт строковое поле из /Info, разбирая обе принятые кодировки.
func (d *Document) infoString(key Name) string {
	info := d.Info()
	if info == nil {
		return ""
	}
	s, ok := d.Resolve(info[key]).(String)
	if !ok {
		return ""
	}
	return decodeTextString(s)
}

// decodeTextString переводит текстовую строку PDF в UTF-8: она бывает в UTF-16BE
// с меткой порядка байтов либо в однобайтовой PDFDocEncoding.
func decodeTextString(s String) string {
	if len(s) >= 2 && s[0] == 0xFE && s[1] == 0xFF {
		return strings.TrimRight(utf16BE(s[2:]), "\x00")
	}
	if utf8.Valid(s) && !hasControl(s) {
		return string(s)
	}
	var b strings.Builder
	for _, c := range s {
		if r := winAnsi[c]; r != 0 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hasControl(s []byte) bool {
	for _, c := range s {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			return true
		}
	}
	return false
}
