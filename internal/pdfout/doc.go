// Package pdfout набирает ответ модели в PDF-документ: markdown на входе,
// готовый файл на выходе.
//
// Пакет намеренно не знает ничего про интерфейс: на вход ему дают текст
// и служебную шапку, на выходе — байты. Благодаря этому весь набор
// проверяется тестами без терминала и без диска, а самая ценная проверка —
// «туда-обратно»: сгенерированный документ читается нашим же разборщиком
// internal/pdf и сверяется с исходным текстом.
//
// # Осторожно с gopdf
//
// Библиотека местами ведёт себя недружелюбно, и обходить это обязательно:
//
//   - GetBytesPdf и WritePdf при ошибке вызывают log.Fatalf (gopdf.go:1099).
//     В TUI это мгновенная смерть процесса без единого слова пользователю
//     и с потерей несохранённого разговора. Здесь используется только
//     GetBytesPdfReturnErr, а файл пишется своими руками.
//   - compilePdf молча проглатывает ошибки записи объектов: испорченный шрифт
//     даст не ошибку, а битый файл. Единственная защита — проверка
//     «туда-обратно» в тестах, выпиливать её нельзя.
//   - MultiCell не умеет переносить текст на новую страницу, поэтому вся
//     раскладка (перенос по словам, разрывы страниц) сделана здесь вручную.
package pdfout

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// MaxSourceBytes — предел исходного текста, как у копирования в буфер обмена.
// Ответ на несколько мегабайт — это почти наверняка выгруженный инструментом
// файл, набирать его в документ бессмысленно.
const MaxSourceBytes = 4 << 20

var (
	// ErrEmpty означает, что набирать нечего.
	ErrEmpty = errors.New("нечего сохранять: текст ответа пуст")

	// ErrTooLarge означает, что текст слишком велик для набора.
	ErrTooLarge = errors.New("текст слишком велик для набора в PDF")

	// ErrExists означает, что файл уже есть и перезапись не разрешена.
	ErrExists = errors.New("файл уже существует")
)

// Meta — служебная шапка документа. Пустые поля не печатаются.
type Meta struct {
	Title    string    // заголовок документа и поле /Title в свойствах файла
	Question string    // текст вопроса; пустой — вопрос не печатается
	Model    string    // имя модели, которая отвечала
	At       time.Time // отметка времени ответа
}

// Options — что и как выводить.
type Options struct {
	Meta Meta

	// WithHeader включает шапку с вопросом, моделью и датой (Shift+F4).
	// Без него в документе только сам ответ (F4).
	WithHeader bool
}

// Result — итог набора.
type Result struct {
	Data  []byte
	Pages int

	// Missing — символы, которых нет во встроенных шрифтах: они заменены
	// в документе на «□». Список нужен, чтобы сказать об этом пользователю:
	// молча терять символы хуже, чем показать замену и признаться.
	Missing []rune
}

// Build набирает документ целиком в памяти.
func Build(src string, opt Options) (*Result, error) {
	if len(src) > MaxSourceBytes {
		return nil, fmt.Errorf("%w: %d байт при пределе %d", ErrTooLarge, len(src), MaxSourceBytes)
	}
	blocks := parseMarkdown(src)
	if len(blocks) == 0 && opt.Meta.Question == "" {
		return nil, ErrEmpty
	}

	p, err := newPainter(defaultTheme(), opt)
	if err != nil {
		return nil, err
	}
	if opt.WithHeader {
		p.docHeader(opt.Meta)
	}
	p.drawBlocks(blocks)

	data, err := p.bytes()
	if err != nil {
		return nil, fmt.Errorf("сборка PDF: %w", err)
	}
	return &Result{Data: data, Pages: p.pages, Missing: p.missingRunes()}, nil
}

// WriteFile набирает документ и пишет его в файл.
//
// Файл создаётся только после успешного набора: ошибка разметки не должна
// оставлять на диске огрызок. Существующий файл без overwrite не затирается.
func WriteFile(path, src string, opt Options, overwrite bool) (*Result, error) {
	res, err := Build(src, opt)
	if err != nil {
		return nil, err
	}

	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrExists, path)
		}
		return nil, err
	}
	defer f.Close()

	if _, err := f.Write(res.Data); err != nil {
		return nil, err
	}
	return res, f.Sync()
}
