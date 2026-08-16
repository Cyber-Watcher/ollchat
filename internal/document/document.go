// Package document — единая точка чтения документов: PDF и книг EPUB.
//
// Форматы разбираются разными пакетами, но пользуются ими одинаково: прочитать
// текст, узнать, сколько там рисунков, вытащить сами рисунки. Этот слой прячет
// различия, чтобы инструмент read_file и команды /add и /addimg не разбирались
// в устройстве каждого формата и не дублировали одну и ту же обвязку.
package document

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/itpro/ollchat/internal/epub"
	"github.com/itpro/ollchat/internal/pdf"
)

// Kind — формат документа.
type Kind string

const (
	KindNone Kind = ""     // не документ
	KindPDF  Kind = "PDF"  //
	KindEPUB Kind = "EPUB" //
)

// Doc — прочитанный документ.
type Doc struct {
	Kind    Kind
	Title   string
	Author  string
	Units   int    // страниц у PDF, разделов у книги
	Unit    string // как называть единицу: «страниц», «разделов»
	Text    string // текст с заголовками единиц
	Figures int    // сколько в тексте меток рисунков
}

// Header — строка сведений о документе, которую видит модель.
//
// canView говорит, включён ли инструмент view_image. Звать к выключенному
// инструменту нельзя: модель принимает его имя за программу и пробует запустить
// через оболочку — проверено на живом сеансе.
func (d *Doc) Header(canView bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Документ %s, %s: %d", d.Kind, d.Unit, d.Units)
	if d.Title != "" {
		fmt.Fprintf(&b, ", заголовок: %s", d.Title)
	}
	if d.Author != "" {
		fmt.Fprintf(&b, ", автор: %s", d.Author)
	}
	if d.Figures > 0 {
		// Подсказка обязана вести к инструменту, а не к команде пользователя:
		// увидев метку без способа посмотреть, модель начинает искать
		// в системе средства распознавания — так и случилось на живом документе.
		fmt.Fprintf(&b, "\nВ тексте %d меток вида [рисунок N.M] — это картинки. Текста в них нет, "+
			"и часто именно в них лежит важное: таблицы цен и схемы нередко свёрстаны "+
			"картинкой целиком. ", d.Figures)
		if canView {
			b.WriteString("Чтобы рассмотреть такую, вызовите инструмент view_image " +
				"с тем же номером, например view_image(path, figure=\"4.1\"). " +
				"Ставить средства распознавания не нужно.")
		} else {
			// Инструмента нет — значит сказать надо не «как посмотреть»,
			// а «посмотреть нечем», иначе модель придумает обходной путь.
			b.WriteString("Показать их нечем: инструмент view_image в этом сеансе выключен " +
				"настройкой agent.tools. Распознать их через оболочку невозможно, внешние " +
				"средства для этого не нужны и не помогут. Скажите пользователю, что в этих " +
				"местах документа есть картинки и что приложить их к вопросу он может " +
				"командой /addimg.")
		}
	}
	return b.String()
}

// Part — одна единица документа: страница PDF или раздел книги EPUB.
//
// Нужна базе знаний: куски текста обязаны знать точный номер страницы, иначе
// ссылку в ответе модели нельзя проверить. Разбирать для этого заголовки
// «── страница N ──» из Doc.Text нельзя по двум причинам: такая строка может
// встретиться в тексте книги о вёрстке, а сами заголовки при индексации
// становятся мусорным термом — проверка на живой книге показала слово
// «страниц» в двадцатке самых частых.
type Part struct {
	Number int    // номер страницы или раздела, начиная с 1
	Title  string // заголовок раздела; у PDF пусто
	Text   string
}

// Image — картинка из документа.
type Image struct {
	Label  string // обозначение, то же что в метке текста: «44.1»
	Width  int
	Height int
	Format string // "jpeg" или "png"
	Data   []byte
}

// ImageOptions ограничивает выборку картинок.
type ImageOptions struct {
	First     int // первая страница или раздел, начиная с 1
	Count     int // сколько их взять; 0 — все
	MinWidth  int
	MinHeight int
	MaxCount  int
}

// Detect определяет формат по началу файла.
func Detect(head []byte) Kind {
	switch {
	case epub.IsEPUB(head):
		return KindEPUB
	case pdf.IsPDF(head):
		return KindPDF
	}
	return KindNone
}

// DetectFile определяет формат файла по содержимому, а не по расширению.
//
// Для EPUB одного заголовка мало: строка типа бывает сжата, и признаком
// служит имя обязательного файла внутри архива. Поэтому читается либо начало
// файла, либо — для архивов — файл целиком.
func DetectFile(path string) Kind {
	f, err := os.Open(path)
	if err != nil {
		return KindNone
	}
	defer f.Close()

	head := make([]byte, 1024)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if k := Detect(head); k != KindNone {
		return k
	}
	if n >= 4 && string(head[:4]) == "PK\x03\x04" {
		if data, err := os.ReadFile(path); err == nil && epub.IsEPUB(data) {
			return KindEPUB
		}
	}
	return KindNone
}

// Read читает документ целиком. Предел maxBytes — на файл: в контекст идёт
// извлечённый текст, но сам файл всё же читается в память.
func Read(path string, maxBytes int64) (*Doc, error) {
	kind, data, err := load(path, maxBytes)
	if err != nil {
		return nil, err
	}
	switch kind {
	case KindPDF:
		res, err := pdf.Extract(data, pdf.Options{})
		if err != nil {
			return nil, describe(err)
		}
		text := res.Text()
		return &Doc{
			Kind: KindPDF, Title: res.Title, Author: res.Author,
			Units: res.TotalPages, Unit: "страниц",
			Text: text, Figures: strings.Count(text, "[рисунок "),
		}, nil
	case KindEPUB:
		res, err := epub.Extract(data, epub.Options{})
		if err != nil {
			return nil, describe(err)
		}
		text := res.Text()
		return &Doc{
			Kind: KindEPUB, Title: res.Title, Author: res.Author,
			Units: res.TotalSections, Unit: "разделов",
			Text: text, Figures: strings.Count(text, "[рисунок "),
		}, nil
	}
	return nil, errors.New("формат файла не распознан")
}

// Parts читает документ поединично: метаданные плюс страницы или разделы
// по отдельности. Поле Doc.Text при этом пустое — склеивать текст незачем,
// вызывающий код разбивает его на куски сам.
func Parts(path string, maxBytes int64) (*Doc, []Part, error) {
	kind, data, err := load(path, maxBytes)
	if err != nil {
		return nil, nil, err
	}
	switch kind {
	case KindPDF:
		res, err := pdf.Extract(data, pdf.Options{})
		if err != nil {
			return nil, nil, describe(err)
		}
		parts := make([]Part, 0, len(res.Pages))
		for _, pg := range res.Pages {
			parts = append(parts, Part{Number: pg.Number, Text: pg.Text})
		}
		return &Doc{
			Kind: KindPDF, Title: res.Title, Author: res.Author,
			Units: res.TotalPages, Unit: "страниц",
		}, parts, nil
	case KindEPUB:
		res, err := epub.Extract(data, epub.Options{})
		if err != nil {
			return nil, nil, describe(err)
		}
		parts := make([]Part, 0, len(res.Sections))
		for _, sec := range res.Sections {
			parts = append(parts, Part{Number: sec.Number, Title: sec.Title, Text: sec.Text})
		}
		return &Doc{
			Kind: KindEPUB, Title: res.Title, Author: res.Author,
			Units: res.TotalSections, Unit: "разделов",
		}, parts, nil
	}
	return nil, nil, errors.New("формат файла не распознан")
}

// Probe быстро определяет, годится ли документ для чтения: формат, метаданные
// и есть ли вообще текстовый слой. Разбирает не больше units единиц, поэтому
// на скане отвечает за миллисекунды вместо полного разбора всех страниц.
//
// Нужен при обходе тысяч книг: скан отличается от книги только тем, что текста
// в нём нет, и узнать это дешевле заранее.
func Probe(path string, maxBytes int64, units int) (*Doc, error) {
	kind, data, err := load(path, maxBytes)
	if err != nil {
		return nil, err
	}
	if units <= 0 {
		units = 5
	}
	switch kind {
	case KindPDF:
		res, err := pdf.Extract(data, pdf.Options{MaxPages: units})
		if err != nil {
			return nil, describe(err)
		}
		return &Doc{
			Kind: KindPDF, Title: res.Title, Author: res.Author,
			Units: res.TotalPages, Unit: "страниц", Text: res.Text(),
		}, nil
	case KindEPUB:
		res, err := epub.Extract(data, epub.Options{MaxSections: units})
		if err != nil {
			return nil, describe(err)
		}
		return &Doc{
			Kind: KindEPUB, Title: res.Title, Author: res.Author,
			Units: res.TotalSections, Unit: "разделов", Text: res.Text(),
		}, nil
	}
	return nil, errors.New("формат файла не распознан")
}

// Images достаёт картинки документа.
func Images(path string, maxBytes int64, opt ImageOptions) ([]Image, error) {
	kind, data, err := load(path, maxBytes)
	if err != nil {
		return nil, err
	}
	var out []Image
	switch kind {
	case KindPDF:
		imgs, err := pdf.ExtractImages(data, pdf.ImageOptions{
			FirstPage: opt.First, MaxPages: opt.Count,
			MinWidth: opt.MinWidth, MinHeight: opt.MinHeight, MaxCount: opt.MaxCount,
		})
		if err != nil {
			return nil, describe(err)
		}
		for _, im := range imgs {
			out = append(out, Image{Label: im.Label(), Width: im.Width, Height: im.Height,
				Format: im.Format, Data: im.Data})
		}
	case KindEPUB:
		imgs, err := epub.ExtractImages(data, epub.ImageOptions{
			FirstSection: opt.First, MaxSections: opt.Count,
			MinWidth: opt.MinWidth, MinHeight: opt.MinHeight, MaxCount: opt.MaxCount,
		})
		if err != nil {
			return nil, describe(err)
		}
		for _, im := range imgs {
			out = append(out, Image{Label: im.Label(), Width: im.Width, Height: im.Height,
				Format: im.Format, Data: im.Data})
		}
	default:
		return nil, errors.New("формат файла не распознан")
	}
	return out, nil
}

// load определяет формат и читает файл, проверив размер.
func load(path string, maxBytes int64) (Kind, []byte, error) {
	kind := DetectFile(path)
	if kind == KindNone {
		return KindNone, nil, errors.New("это не документ PDF и не книга EPUB")
	}
	info, err := os.Stat(path)
	if err != nil {
		return KindNone, nil, err
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return KindNone, nil, fmt.Errorf("документ слишком велик: %d байт, предел %d (sandbox.max_pdf_mb)",
			info.Size(), maxBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return KindNone, nil, err
	}
	return kind, data, nil
}

// describe дополняет ошибки разбора советом, что с этим делать: модели важно
// понять, что дело не в неудачном вызове, а в самом документе.
func describe(err error) error {
	switch {
	case errors.Is(err, pdf.ErrNoText):
		return fmt.Errorf("%w: страницы этого документа — изображения, "+
			"текст можно получить только распознаванием; сами страницы можно "+
			"приложить командой /addimg и показать модели с возможностью vision", err)
	case errors.Is(err, epub.ErrNoText):
		return fmt.Errorf("%w: в главах книги нет текста", err)
	}
	return err
}
