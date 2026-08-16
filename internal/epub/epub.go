// Package epub извлекает текст и картинки из книг формата EPUB.
//
// EPUB устроен куда проще PDF: это ZIP-архив, внутри которого лежат главы
// в XHTML, список глав в файле OPF и картинки обычными файлами. Поэтому здесь
// нет ни разбора шрифтов, ни восстановления раскладки по координатам —
// структура текста задана разметкой и её достаточно прочитать.
//
// Разбор, как и у PDF, сделан своим кодом на стандартной библиотеке:
// archive/zip и encoding/xml. Никаких внешних программ не требуется.
package epub

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// ErrNotEPUB возвращается, если файл не похож на книгу EPUB.
var ErrNotEPUB = errors.New("файл не является книгой EPUB")

// ErrNoText возвращается, когда в книге нет ни одной главы с текстом.
var ErrNoText = errors.New("в книге не нашлось текста")

// Options ограничивает выборку разделов.
type Options struct {
	FirstSection int // с какого раздела начать, начиная с 1
	MaxSections  int // сколько разделов взять; 0 — все
}

// Section — один раздел книги: то, что в EPUB лежит отдельным файлом главы.
type Section struct {
	Number int
	Title  string
	Text   string
}

// Result — итог извлечения.
type Result struct {
	Sections      []Section
	TotalSections int
	Title         string
	Author        string
	Truncated     bool
}

// Text собирает текст разделов с заголовками.
func (r *Result) Text() string {
	var b strings.Builder
	for i, s := range r.Sections {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if s.Title != "" {
			fmt.Fprintf(&b, "── раздел %d: %s ──\n", s.Number, s.Title)
		} else {
			fmt.Fprintf(&b, "── раздел %d ──\n", s.Number)
		}
		if s.Text == "" {
			b.WriteString("(пусто)")
			continue
		}
		b.WriteString(s.Text)
	}
	return b.String()
}

// IsEPUB сообщает, похожи ли данные на книгу EPUB.
//
// По спецификации файл mimetype идёт первым и не сжимается, так что строка
// типа обычно лежит открытым текстом в начале. Но так делают не все: часть
// книг сжимает и его, и тогда признака в заголовке нет. Тогда проверяется
// наличие обязательного META-INF/container.xml — имена файлов в оглавлении
// архива всегда хранятся открыто.
func IsEPUB(data []byte) bool {
	if len(data) < 4 || !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return false
	}
	head := data
	if len(head) > 256 {
		head = head[:256]
	}
	if bytes.Contains(head, []byte("application/epub+zip")) {
		return true
	}
	return bytes.Contains(data, []byte("META-INF/container.xml"))
}

// book — открытая книга.
type book struct {
	zip   *zip.Reader
	files map[string]*zip.File // путь внутри архива → файл
}

func open(data []byte) (*book, error) {
	if !IsEPUB(data) {
		return nil, ErrNotEPUB
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("архив книги: %w", err)
	}
	b := &book{zip: zr, files: make(map[string]*zip.File, len(zr.File))}
	for _, f := range zr.File {
		b.files[cleanPath(f.Name)] = f
	}
	return b, nil
}

// read читает файл из архива по пути внутри него.
func (b *book) read(name string) ([]byte, error) {
	f, ok := b.files[cleanPath(name)]
	if !ok {
		return nil, fmt.Errorf("в книге нет файла %s", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	// Предел на случай испорченного архива: распакованный размер объявляет
	// сам файл, и верить ему нельзя.
	return io.ReadAll(io.LimitReader(rc, 64<<20))
}

func cleanPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean(strings.TrimPrefix(p, "./"))
	return strings.TrimPrefix(p, "/")
}

// Extract извлекает текст книги.
func Extract(data []byte, opt Options) (res *Result, err error) {
	// Повреждённая книга не должна ронять приложение — см. recover.go.
	defer catch("разбор EPUB", &err)

	b, err := open(data)
	if err != nil {
		return nil, err
	}
	pkg, err := b.readPackage()
	if err != nil {
		return nil, err
	}
	if len(pkg.spine) == 0 {
		return nil, errors.New("в книге не нашлось ни одной главы")
	}

	first := opt.FirstSection
	if first < 1 {
		first = 1
	}
	if first > len(pkg.spine) {
		return nil, fmt.Errorf("в книге %d разделов, запрошено начало с %d", len(pkg.spine), first)
	}
	last := len(pkg.spine)
	if opt.MaxSections > 0 && first-1+opt.MaxSections < last {
		last = first - 1 + opt.MaxSections
	}

	res = &Result{
		TotalSections: len(pkg.spine),
		Truncated:     last < len(pkg.spine),
		Title:         pkg.title,
		Author:        pkg.author,
	}
	nonEmpty := 0
	for i := first - 1; i < last; i++ {
		href := pkg.spine[i]
		raw, err := b.read(href)
		if err != nil {
			res.Sections = append(res.Sections, Section{Number: i + 1})
			continue
		}
		doc := parseHTML(raw, i+1)
		if strings.TrimSpace(doc.text) != "" {
			nonEmpty++
		}
		title := doc.title
		if title == "" {
			title = pkg.titles[href]
		}
		res.Sections = append(res.Sections, Section{Number: i + 1, Title: title, Text: doc.text})
	}
	if nonEmpty == 0 {
		return nil, ErrNoText
	}
	return res, nil
}

// ExtractFile читает книгу с диска и извлекает текст.
func ExtractFile(path string, opt Options) (*Result, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	return Extract(data, opt)
}
