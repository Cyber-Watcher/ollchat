package document

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Текстовые документы: markdown и обычный текст.
//
// Зачем они здесь. PDF и EPUB — чужие документы, которые читают целиком
// и редко. Документация проекта живёт в .md, меняется каждый день, и искать
// по ней нужно так же, как по книгам: вопросом, а не обходом файлов. Разница
// в единице ссылки — у книги страница, у текстового файла строка: по ней
// сразу открывают нужное место в редакторе.
//
// Формат определяется по расширению, а не по содержимому. Для PDF и EPUB
// содержимое надёжнее (у них есть подпись в начале файла), а текст от текста
// ничем не отличается: у .md и .txt один и тот же признак — «это буквы».

// MaxTextLines — предел длины текстового документа в строках.
//
// Номер строки хранится в двоичной записи куска как uint16 (internal/kb),
// то есть больше 65 535 не помещается. Молча обрезать нельзя: ссылки на конец
// файла показывали бы 65 535 у каждого куска, и проверить их стало бы
// невозможно — а непроверяемая ссылка хуже отсутствующей.
const MaxTextLines = 65535

// ErrTooManyLines — файл длиннее предела. Отдельная ошибка, чтобы вызывающий
// мог отличить «слишком длинный» от «нечитаемый» и сказать человеку, что
// с этим делать.
var ErrTooManyLines = errors.New("файл длиннее предела строк")

// TextExt сообщает, берём ли мы файл с таким расширением как текстовый.
func TextExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".txt", ".text":
		return true
	}
	return false
}

// isMarkdown отличает разметку от простого текста: у markdown есть заголовки.
func isMarkdown(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// readText читает текстовый файл и режет его на строки-части.
//
// Одна строка — одна часть. Это кажется расточительным, но именно так
// нарезчик получает точный номер каждой строки и может собрать кусок,
// честно знающий, где он начался и где кончился. Сами части ничего не стоят:
// это срезы одной строки, а не копии.
func readText(path string, data []byte) (*Doc, []Part, error) {
	if !utf8.Valid(data) {
		return nil, nil, errors.New(
			"файл не в кодировке UTF-8 — перекодируйте его (iconv) и повторите")
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")

	if len(lines) > MaxTextLines {
		// Сообщение короткое намеренно: /kb показывает причину пропуска
		// одной строкой, и совет обязан в неё поместиться.
		return nil, nil, fmt.Errorf(
			"%w — в нём %d при пределе %d, разбейте по разделам на несколько файлов",
			ErrTooManyLines, len(lines), MaxTextLines)
	}

	md := isMarkdown(path)
	parts := make([]Part, 0, len(lines))
	var heading string // ближайший заголовок markdown выше этой строки
	inFence := false
	for i, l := range lines {
		if md {
			if strings.HasPrefix(strings.TrimSpace(l), "```") {
				inFence = !inFence
			} else if !inFence {
				if h := mdHeading(l); h != "" {
					heading = h
				}
			}
		}
		parts = append(parts, Part{Number: i + 1, Title: heading, Text: l})
	}

	kind := KindText
	if md {
		kind = KindMarkdown
	}
	d := &Doc{
		Kind: kind, Title: textTitle(path, lines, md),
		Units: len(lines), Unit: "строк", Text: text,
	}
	d.Year, d.YearSrc = PickYear(path, headOf(text), 0)
	return d, parts, nil
}

// mdHeading возвращает текст заголовка markdown или пустую строку.
func mdHeading(line string) string {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "#") {
		return ""
	}
	s = strings.TrimLeft(s, "#")
	if s == "" || !strings.HasPrefix(s, " ") {
		return "" // «###» без текста или «#hashtag» — не заголовок
	}
	return strings.TrimSpace(s)
}

// textTitle — как называть документ в выдаче поиска.
//
// У markdown берём первый заголовок первого уровня: он и есть название.
// Не нашли — имя файла: оно всяко понятнее, чем первая строка текста.
func textTitle(path string, lines []string, md bool) string {
	if md {
		for i, l := range lines {
			if i > 40 {
				break
			}
			if h := mdHeading(l); h != "" && strings.HasPrefix(strings.TrimSpace(l), "# ") {
				return h
			}
		}
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}
