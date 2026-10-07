package redact

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/pdfout"
)

// Четыре итога обработки скана и их имена.
//
// Пользователи просят (06.10.2026) четыре файла: PDF с замазанными данными
// и .md без персональных данных — чтобы отдавать наружу, — а также
// распознанные копии: .md со всем текстом и текстовый PDF с таблицами,
// который можно читать, искать и копировать. Разбор форматов и имена общие
// у ключа --scan-redact и инструмента scan_redact: прежде каждый разбирал
// formats сам, подстрокой, и «ocr-pdf» прочёлся бы ещё и как «pdf».

// Formats — какие файлы сделать.
type Formats struct {
	PDF    bool // такой же PDF с замазанными данными
	MD     bool // .md без персональных данных
	OCRPDF bool // текстовый PDF распознанного текста, С персональными данными
	OCRMD  bool // .md распознанного текста, С персональными данными
}

// DefaultFormats — замазанный PDF и обезличенный .md. Распознанные копии
// со всеми персональными данными делаются только по явной просьбе (слово
// владельца 07.10.2026): прежде по умолчанию делались все четыре файла,
// и копия медицинского документа со всеми данными ложилась рядом
// с исходником при каждом запуске, даже когда её никто не просил.
const DefaultFormats = "pdf,md"

// FormatsHelp — подсказка к ключу и к описанию инструмента.
const FormatsHelp = "через запятую: pdf (замазанный PDF) и md (.md без персональных данных) — " +
	"эти два по умолчанию; ocr-pdf (текстовый PDF распознанного, со ВСЕМИ персональными данными) " +
	"и ocr-md (.md распознанного, со ВСЕМИ персональными данными) — только по просьбе; " +
	"ocr — оба распознанных, all — все четыре"

// ParseFormats разбирает список форматов; пустой — DefaultFormats.
func ParseFormats(s string) (Formats, error) {
	var f Formats
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		s = DefaultFormats
	}
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '+' }) {
		switch p {
		case "pdf":
			f.PDF = true
		case "md":
			f.MD = true
		case "ocr-pdf", "ocr_pdf", "ocrpdf":
			f.OCRPDF = true
		case "ocr-md", "ocr_md", "ocrmd":
			f.OCRMD = true
		case "ocr":
			f.OCRPDF, f.OCRMD = true, true
		case "all":
			f = Formats{true, true, true, true}
		default:
			return Formats{}, fmt.Errorf("неизвестный формат %q; %s", p, FormatsHelp)
		}
	}
	return f, nil
}

// Outputs — пути итогов; пустой путь — этот файл не нужен.
type Outputs struct{ PDF, MD, OCRPDF, OCRMD string }

// Stem — основа имени итогов: имя исходника без расширения и без пробелов
// на конце. Образец 06.10.2026 назывался «15-006-test .pdf» — с пробелом
// перед точкой, и итоги выходили бы «15-006-test .redacted.pdf».
func Stem(path string) string {
	return strings.TrimRight(strings.TrimSuffix(path, filepath.Ext(path)), " \t")
}

// DefaultOutputs — имена рядом с исходником: <имя>.redacted.pdf,
// <имя>.redacted.md, <имя>.ocr.pdf, <имя>.ocr.md.
func DefaultOutputs(in string, f Formats) Outputs {
	stem := Stem(in)
	var o Outputs
	if f.PDF {
		o.PDF = stem + ".redacted.pdf"
	}
	if f.MD {
		o.MD = stem + ".redacted.md"
	}
	if f.OCRPDF {
		o.OCRPDF = stem + ".ocr.pdf"
	}
	if f.OCRMD {
		o.OCRMD = stem + ".ocr.md"
	}
	return o
}

// outputNames — как назвать итог в отказе и в сводке.
var outputNames = [4]string{"PDF с замазанными данными", ".md без персональных данных",
	"текстовый PDF распознанного", ".md распознанного"}

func (o Outputs) all() [4]string { return [4]string{o.PDF, o.MD, o.OCRPDF, o.OCRMD} }

// List — заказанные пути по порядку: pdf, md, ocr-pdf, ocr-md.
func (o Outputs) List() []string {
	var out []string
	for _, p := range o.all() {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// unverifiedMark вставляется перед расширением обезличенного итога, в котором
// проверка нашла скрытое: «скан.redacted.UNVERIFIED.pdf».
const unverifiedMark = ".UNVERIFIED"

// Unverified — имена итогов на случай, когда проверка нашла скрытое в самих
// обезличенных файлах: замазанный PDF и .md получают пометку UNVERIFIED.
// Прежде они ложились под обычными именами, и файл, который нельзя
// показывать наружу, ничем не отличался от проверенного — только код
// выхода 3, которого в папке не видно. Распознанные копии и так со всеми
// данными: их имена не меняются.
func (o Outputs) Unverified() Outputs {
	mark := func(p string) string {
		if p == "" {
			return ""
		}
		ext := filepath.Ext(p)
		return strings.TrimSuffix(p, ext) + unverifiedMark + ext
	}
	o.PDF, o.MD = mark(o.PDF), mark(o.MD)
	return o
}

// Check — отказы до работы. Исходник не перезаписывается: запись идёт
// переименованием, и оригинал скана пропал бы без следа. Два итога не
// пишутся в один файл: второй затёр бы первый, и обезличенный PDF мог бы
// оказаться под распознанным, со всеми данными. Проверяются оба набора
// имён — и обычный, и с пометкой UNVERIFIED: исходник «скан.UNVERIFIED.pdf»
// иначе затёрся бы итогом -out-pdf скан.pdf, не прошедшим проверку.
func (o Outputs) Check(in string) error {
	if err := o.check(in); err != nil {
		return err
	}
	return o.Unverified().check(in)
}

func (o Outputs) check(in string) error {
	abs := func(p string) string {
		if a, err := filepath.Abs(p); err == nil {
			return a
		}
		return filepath.Clean(p)
	}
	src := abs(in)
	all := o.all()
	any := false
	for i, p := range all {
		if p == "" {
			continue
		}
		any = true
		if abs(p) == src {
			return fmt.Errorf("%s: путь совпадает с исходным документом — исходник не перезаписывается", outputNames[i])
		}
		for j := i + 1; j < len(all); j++ {
			if all[j] != "" && abs(all[j]) == abs(p) {
				return fmt.Errorf("%s и %s указывают на один файл — второй затёр бы первый",
					outputNames[i], outputNames[j])
			}
		}
	}
	if !any {
		return fmt.Errorf("не заказано ни одного файла")
	}
	return nil
}

// Written — записанный итог.
type Written struct {
	What  string // для человека: «PDF с замазанными данными»
	Path  string
	Bytes int
	Note  string // оговорка: символы, которых нет в шрифтах
}

// WriteOutputs пишет заказанные файлы. Распознанные копии берутся из
// res.OCRMD — в них всё прочитанное, с персональными данными; вызывающий
// отдаёт модели только пути и размеры, но не их текст. Если проверка нашла
// скрытое, обезличенные итоги пишутся под именами Unverified.
func WriteOutputs(o Outputs, title string, pages []Page, res *Result) ([]Written, error) {
	leak := !res.Check.OK()
	if leak {
		o = o.Unverified()
	}
	var out []Written
	put := func(i int, path string, data []byte, note string) error {
		if err := WriteFile(path, data); err != nil {
			return err
		}
		what := outputNames[i]
		if leak && i < 2 {
			what += " — НЕ ПРОВЕРЕН: проверка нашла в нём скрытое"
		}
		out = append(out, Written{What: what, Path: path, Bytes: len(data), Note: note})
		return nil
	}
	if o.PDF != "" {
		data, err := PDF(pages, res.Redacted)
		if err != nil {
			return out, err
		}
		if err := put(0, o.PDF, data, ""); err != nil {
			return out, err
		}
	}
	if o.MD != "" {
		if err := put(1, o.MD, []byte(res.MD), ""); err != nil {
			return out, err
		}
	}
	if o.OCRPDF != "" {
		data, note, err := TextPDF(title, res.OCRMD)
		if err != nil {
			return out, err
		}
		if err := put(2, o.OCRPDF, data, note); err != nil {
			return out, err
		}
	}
	if o.OCRMD != "" {
		if err := put(3, o.OCRMD, []byte(res.OCRMD), ""); err != nil {
			return out, err
		}
	}
	return out, nil
}

// TextPDF набирает .md в PDF с настоящим текстом и таблицами — тем же
// набором, что сохраняет ответы модели (internal/pdfout). Вторым значением —
// оговорка о символах, которых нет во встроенных шрифтах (они заменены «□»).
func TextPDF(title, md string) ([]byte, string, error) {
	res, err := pdfout.Build(md, pdfout.Options{Meta: pdfout.Meta{Title: title}})
	if err != nil {
		return nil, "", fmt.Errorf("текстовый PDF: %w", err)
	}
	note := ""
	if len(res.Missing) > 0 {
		note = fmt.Sprintf("символов нет во встроенных шрифтах, заменены на «□»: %q", string(res.Missing))
	}
	return res.Data, note, nil
}
