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
//
// id вступает в силу, когда событие отдано (пустой строкой), а не когда
// прочитано поле: так велит стандарт — поле пишет в «буфер id», отдача
// события переносит буфер в lastID. id события, оборванного концом потока,
// не в счёт: иначе Last-Event-ID назвал бы событие, которого клиент не
// получил, и сервер, повторяющий всё после него, этого события не повторил
// бы (аудит 23.10.2026).
type sseReader struct {
	sc     *bufio.Scanner
	first  bool
	idBuf  string // id из полей текущего события; в lastID — при отдаче
	lastID string
	retry  time.Duration
}

func newSSE(r io.Reader) *sseReader {
	s := &sseReader{}
	s.reset(r)
	return s
}

// reset — тот же разборщик на новом теле (после переподключения).
//
// Буфер id начинается с lastID, а не пустым, как у стандарта: событие без id
// в продолжении потока иначе стёрло бы номер, по которому переподключаться
// в следующий раз.
func (s *sseReader) reset(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxBody+1)
	sc.Split((&lineSplitter{}).split)
	s.sc = sc
	s.first = true
	s.idBuf = s.lastID
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
			s.lastID = s.idBuf // и у события без данных: затравка из одного id
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
				s.idBuf = value
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

// lineSplitter — деление на строки по CRLF, LF и одинокому CR.
//
// Помнит, сколько байт незаконченной строки уже просмотрено: bufio.Scanner
// после каждого дочитывания подаёт буфер с её начала, и поиск конца с начала
// делал разбор одной длинной строки квадратичным (аудит 23.10.2026, находка
// 6: 16 МиБ одной строкой data — около 2,9 с процессора; предел 32 МиБ не
// успевал сработать раньше срока, и ошибка выходила «не ответил» с меткой
// ErrUnavailable). Просмотренное сбрасывается, когда строка отдана: Scanner
// сдвигает начало буфера только тогда.
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
