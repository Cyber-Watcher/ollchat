package oai

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// Разбор потока Server-Sent Events — та часть стандарта (WHATWG HTML, «event
// stream interpretation»), которая нужна потоку чата: строки кончаются на
// CRLF, LF или одинокий CR; пустая строка отдаёт событие; строка с двоеточия
// — комментарий (запись Ш1 llamacpp/plain начинается строкой «:»);
// несколько строк data склеиваются через перевод строки; событие без data не
// отдаётся; событие, оборванное концом потока, отбрасывается. Имя события, id
// и retry чату не нужны.
//
// Тот же разбор есть в internal/mcpclient/sse.go, но не экспортирован, а
// этот этап пакеты вне internal/oai не правит. Деление на строки — его же
// приём (lineSplitter): без памяти о просмотренном длинная строка data
// разбиралась квадратично (аудит 23.10.2026, находка 6).

// maxSSELine — предел одной строки потока. Поток в целом не ограничен (честный
// ответ идёт сколь угодно долго), а одна строка — один кусок JSON, и строка
// в 64 МиБ — уже не ответ модели.
const maxSSELine = 64 << 20

// byteOrderMark — U+FEFF, которым поток вправе начинаться; стандарт велит его
// пропустить.
var byteOrderMark = string(rune(0xFEFF))

// sseReader — разборщик потока.
type sseReader struct {
	sc    *bufio.Scanner
	first bool
}

func newSSE(r io.Reader) *sseReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxSSELine)
	sc.Split((&lineSplitter{}).split)
	return &sseReader{sc: sc, first: true}
}

// next — данные следующего события; io.EOF — поток кончился.
func (s *sseReader) next() (string, error) {
	var (
		data strings.Builder
		has  bool
	)
	for s.sc.Scan() {
		line := s.sc.Text()
		if s.first {
			line = strings.TrimPrefix(line, byteOrderMark)
			s.first = false
		}
		if line == "" {
			if !has {
				continue
			}
			return strings.TrimSuffix(data.String(), "\n"), nil
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		if field == "data" {
			data.WriteString(value)
			data.WriteByte('\n')
			has = true
		}
	}
	if err := s.sc.Err(); err != nil {
		return "", err
	}
	return "", io.EOF
}

// lineSplitter — деление на строки по CRLF, LF и одинокому CR. Помнит, сколько
// байт незаконченной строки уже просмотрено: bufio.Scanner после каждого
// дочитывания подаёт буфер с её начала.
type lineSplitter struct {
	seen int
}

func (l *lineSplitter) split(data []byte, atEOF bool) (int, []byte, error) {
	from := l.seen
	if from > len(data) {
		from = 0
	}
	i := bytes.IndexAny(data[from:], "\r\n")
	if i < 0 {
		if atEOF && len(data) > 0 {
			l.seen = 0
			return len(data), data, nil
		}
		l.seen = len(data)
		return 0, nil, nil
	}
	i += from
	if data[i] == '\n' {
		l.seen = 0
		return i + 1, data[:i], nil
	}
	// CR: за ним может быть LF, а может не прийти ещё ничего.
	if i+1 < len(data) {
		l.seen = 0
		if data[i+1] == '\n' {
			return i + 2, data[:i], nil
		}
		return i + 1, data[:i], nil
	}
	if atEOF {
		l.seen = 0
		return i + 1, data[:i], nil
	}
	l.seen = i // CR последним байтом: дочитав, смотреть снова с него
	return 0, nil, nil
}
