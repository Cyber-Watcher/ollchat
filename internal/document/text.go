package document

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
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

// TextExt сообщает, берём ли мы файл с таким расширением как ДОКУМЕНТ-текст.
//
// Код сюда не входит намеренно. Первая редакция правки 25.09.2026 добавила
// к нему `.go`, и это молча поменяло совсем другое поведение: `read_file`
// на собственном исходнике пошёл путём документа и стал помечать вывод как
// ЧУЖОЙ текст (`Plan.Foreign`) — то есть код проекта подавался модели так же,
// как страница из сети. Поймали три чужих теста, которые это решение стерегли.
// Отсюда разделение: TextExt — «документ», IndexExt — «можно положить
// в базу знаний». Вопросы разные, и смешивать их нельзя.
func TextExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".txt", ".text":
		return true
	}
	return false
}

// IndexExt — можно ли положить файл в коллекцию базы знаний.
//
// Шире TextExt ровно на код: искать по своему коду вопросом полезно, а вот
// считать его чужим документом при чтении — нет (см. TextExt).
func IndexExt(path string) bool { return TextExt(path) || CodeExt(path) }

// CodeExt — файл с исходным кодом: берётся как текст, но заголовком куска
// служит объявление, а не заголовок раздела.
//
// **Зачем код в базе знаний.** Знание проекта живёт не только в `docs/`:
// правило бывает записано только в скрипте обвязки, а почему сделано так —
// в комментарии над функцией. 25.09.2026 разбор сторожа карты, обёртки `graph`
// и пакетов `internal/` шёл `grep`'ом, потому что искать по ним было нечем.
// Цена замерена в тот же день: весь код проекта — около 6 350 кусков против
// 94 171 в коллекции документации, то есть прибавка 7 % и две минуты карты
// на векторы.
func CodeExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".sh", ".bash", ".py":
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
	code := CodeExt(path)
	parts := make([]Part, 0, len(lines))
	var heading string // ближайший заголовок markdown (или объявление) выше строки
	inFence := false
	for i, l := range lines {
		switch {
		case md:
			if strings.HasPrefix(strings.TrimSpace(l), "```") {
				inFence = !inFence
			} else if !inFence {
				if h := mdHeading(l); h != "" {
					heading = h
				}
			}
		case code:
			// У кода роль заголовка раздела играет объявление: без него
			// ссылка «строки 120–140» не говорит, что это за место.
			if h := codeHeading(l, filepath.Ext(path)); h != "" {
				heading = h
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

// Объявления, по которым узнаётся «раздел» в коде. Языков ровно три — те,
// на которых написан проект; остальное сюда не попадает, потому что CodeExt
// таких расширений не берёт.
var (
	reGoFunc   = regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)`)
	reGoType   = regexp.MustCompile(`^type\s+([A-Za-z_]\w*)`)
	reShFunc   = regexp.MustCompile(`^(?:function\s+)?([A-Za-z_][\w-]*)\s*\(\)`)
	rePyDefCls = regexp.MustCompile(`^\s*(def|class)\s+([A-Za-z_]\w*)`)
)

// codeHeading — объявление, начинающееся на этой строке, или пустая строка.
//
// У Go и оболочки берутся только объявления с начала строки: вложенное
// замыкание — не «раздел», и его имя сбивало бы ссылку. У Python отступ
// допускается намеренно: методы класса живут с отступом, и именно они
// интересны тому, кто ищет.
func codeHeading(line, ext string) string {
	switch strings.ToLower(ext) {
	case ".go":
		if m := reGoFunc.FindStringSubmatch(line); m != nil {
			return "func " + m[1]
		}
		if m := reGoType.FindStringSubmatch(line); m != nil {
			return "type " + m[1]
		}
	case ".sh", ".bash":
		if m := reShFunc.FindStringSubmatch(line); m != nil {
			return m[1] + "()"
		}
	case ".py":
		if m := rePyDefCls.FindStringSubmatch(line); m != nil {
			return m[1] + " " + m[2]
		}
	}
	return ""
}

// textTitle — как называть документ в выдаче поиска.
//
// У markdown берём первый заголовок первого уровня: он и есть название.
// Не нашли — имя файла: оно всяко понятнее, чем первая строка текста.
//
// У кода к имени файла приписывается каталог: `main.go` в проекте два десятка
// (по одному на каждый инструмент `privatescripts/`), и выдача из одних
// «main» не сказала бы, о котором речь.
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
	if CodeExt(path) {
		if dir := filepath.Base(filepath.Dir(path)); dir != "." && dir != string(filepath.Separator) {
			return dir + "/" + filepath.Base(path)
		}
		return filepath.Base(path)
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}
