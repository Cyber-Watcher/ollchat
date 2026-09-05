package pdf

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
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
	// Date — год из /Info /CreationDate или /ModDate. Это дата **файла**,
	// а не издания: у переизданной книги она часто новее, у отсканированной
	// старой — новее лет на двадцать. Поэтому источник самый ненадёжный
	// и используется последним (см. internal/document/year.go).
	Date      int
	Truncated bool // страниц больше, чем показано

	// Lossy — часть шрифтов сопоставить не удалось, и текст неполон.
	// Не ошибка: так бывает у книг со значковыми и декоративными шрифтами.
	// Но сказать об этом надо — иначе пропажа куска текста выглядит
	// как «в книге про это не написано».
	Lossy bool
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
		Date:       pdfYear(doc.infoString("CreationDate"), doc.infoString("ModDate")),
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
	// Годен ли добытый текст — решается по самому тексту, а не по доле
	// сопоставленных глифов.
	//
	// **Замер 30.08.2026.** Прежнее правило «несопоставленных больше, чем
	// показанных» отвергало книгу CompTIA Linux+ (906 страниц), из которой
	// извлекалось **821 210 знаков связного английского текста**: в ней треть
	// глифов сопоставилась, а две трети пришлись на декоративные и значковые
	// шрифты, которых в тексте и не должно быть. Книга открывается любым
	// просмотрщиком, а ollchat объявлял её нечитаемой — и человек, положивший
	// её в библиотеку, не получал по ней ни одного ответа.
	//
	// Доля глифов вообще плохая мера: она считает то, что нарисовано, а нам
	// важно то, что прочитано. Поэтому смотрим на добытые строки.
	//
	// Подозрение возникает только когда несопоставленных больше сопоставленных.
	// Пока их меньше — документ обычный, и придираться не к чему: короткая
	// записка на пять слов такой же законный документ, как книга на девятьсот
	// страниц, и объёмом её мерить нельзя.
	if ex.unmapped > ex.shown {
		if !usableText(res.Pages) {
			return nil, ErrGarbledText
		}
		res.Lossy = true
	}
	return res, nil
}

// usableText решает, годится ли добытый текст для чтения.
//
// Два признака, и оба нужны:
//
//   - **объём**: несколько знаков на страницу — это подписи к картинкам,
//     а не текст книги;
//   - **похожесть на язык**: доля букв и пробелов. У испорченной раскладки
//     получается поток редких символов, у настоящего текста — буквы.
//
// Пороги нарочно снисходительные. Ошибка в одну сторону стоит зря разобранной
// книги, которую человек увидит и уберёт сам; ошибка в другую — молча
// потерянная книга, о которой никто не узнает. Вторая дороже.
func usableText(pages []Page) bool {
	var runes, letters, spaces int
	for _, p := range pages {
		for _, r := range p.Text {
			runes++
			switch {
			case unicode.IsLetter(r) || unicode.IsDigit(r):
				letters++
			case unicode.IsSpace(r):
				spaces++
			}
		}
	}
	if runes == 0 || len(pages) == 0 {
		return false
	}
	// Объём проверяется только у подозрительного документа, и порог общий,
	// а не на страницу: у книги с несопоставленными шрифтами текст обычно
	// собирается неравномерно — десять пустых страниц и одна полная.
	// Двести знаков на весь документ — меньше абзаца; на этом настаивать
	// не на чем.
	if runes < 200 {
		return false
	}
	// Половина знаков — буквы и пробелы: у осмысленного текста их куда больше,
	// у мусора из несопоставленных кодов — заметно меньше.
	return 2*(letters+spaces) >= runes
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

// pdfYear достаёт год из дат /Info. Формат по стандарту — «D:20190312145601+03'00'»,
// но встречается и человеческий вид: разбирается первое четырёхзначное число,
// похожее на год.
func pdfYear(dates ...string) int {
	for _, d := range dates {
		if y := firstYear(d); y > 0 {
			return y
		}
	}
	return 0
}

// firstYear находит в строке первое правдоподобное четырёхзначное число.
// Правдоподобие проверяется здесь грубо, а окончательно — в document.
func firstYear(s string) int {
	for i := 0; i+4 <= len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			continue
		}
		n := 0
		ok := true
		for j := i; j < i+4; j++ {
			if s[j] < '0' || s[j] > '9' {
				ok = false
				break
			}
			n = n*10 + int(s[j]-'0')
		}
		if ok && n >= 1900 && n <= 2100 {
			return n
		}
	}
	return 0
}
