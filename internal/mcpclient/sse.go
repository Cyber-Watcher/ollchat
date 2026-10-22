package mcpclient

import (
	"bufio"
	"bytes"
	"io"
	"strconv"
	"strings"
	"time"
)

// Разбор потока Server-Sent Events — ровно та часть стандарта (WHATWG HTML,
// «event stream interpretation»), которая нужна клиенту MCP.
//
// Строки кончаются на CRLF, LF или одинокий CR. Пустая строка отдаёт событие;
// строка с двоеточия — комментарий; поле без двоеточия — поле с пустым
// значением; один пробел после двоеточия срезается. Несколько строк data
// склеиваются через перевод строки — сервер вправе разбить длинный JSON.
// Событие с пустым буфером data не отдаётся; событие, оборванное концом
// потока, отбрасывается. Имя события (event) клиенту не нужно: в Streamable
// HTTP каждое событие с данными — сообщение JSON-RPC.

// byteOrderMark — U+FEFF, которым поток вправе начинаться; стандарт велит его
// пропустить. Записан числом: сам знак в исходнике компилятор отвергает
// (illegal byte order mark), а невидимый знак в строке легко потерять.
var byteOrderMark = string(rune(0xFEFF))

// sseEvent — одно событие потока.
type sseEvent struct {
	name string
	data string
}

// sseReader — разборщик потока. lastID и retry переживают reset: по ним
// переподключаются к потоку, закрытому сервером.
type sseReader struct {
	sc     *bufio.Scanner
	first  bool
	lastID string
	retry  time.Duration
}

func newSSE(r io.Reader) *sseReader {
	s := &sseReader{}
	s.reset(r)
	return s
}

// reset — тот же разборщик на новом теле (после переподключения).
func (s *sseReader) reset(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxBody+1)
	sc.Split(sseLines)
	s.sc = sc
	s.first = true
}

// next — следующее событие с данными; io.EOF — поток кончился.
func (s *sseReader) next() (sseEvent, error) {
	var (
		name string
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
				name = ""
				continue
			}
			d := strings.TrimSuffix(data.String(), "\n")
			return sseEvent{name: name, data: d}, nil
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			name = value
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
			has = true
		case "id":
			if !strings.ContainsRune(value, 0) {
				s.lastID = value
			}
		case "retry":
			if value != "" && strings.Trim(value, "0123456789") == "" {
				if ms, err := strconv.Atoi(value); err == nil {
					s.retry = time.Duration(ms) * time.Millisecond
				}
			}
		}
	}
	if err := s.sc.Err(); err != nil {
		return sseEvent{}, err
	}
	return sseEvent{}, io.EOF
}

// sseLines — деление на строки по CRLF, LF и одинокому CR.
func sseLines(data []byte, atEOF bool) (int, []byte, error) {
	i := bytes.IndexAny(data, "\r\n")
	if i < 0 {
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	}
	if data[i] == '\n' {
		return i + 1, data[:i], nil
	}
	// CR: за ним может быть LF, а может не прийти ещё ничего.
	if i+1 < len(data) {
		if data[i+1] == '\n' {
			return i + 2, data[:i], nil
		}
		return i + 1, data[:i], nil
	}
	if atEOF {
		return i + 1, data[:i], nil
	}
	return 0, nil, nil
}
