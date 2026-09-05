// Package pdf извлекает текстовый слой из документов PDF.
//
// Разбор сделан своим кодом намеренно: приложение — один бинарь, и требовать
// от пользователя pdftotext, python или установку библиотек нельзя. Внешние
// утилиты в системе может быть нечем поставить, а модель, столкнувшись с их
// отсутствием, начинает перебирать способы установки и выжигает лимит итераций.
//
// Извлекается именно текстовый слой. Отсканированные документы, где текста нет
// и есть только картинка страницы, честно распознаются как таковые: пакет
// сообщает об этом отдельной ошибкой, а не возвращает пустоту.
package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
)

// Name — имя PDF, записывается со слешем: /Type, /Font.
type Name string

// Ref — ссылка на косвенный объект, «12 0 R».
type Ref struct{ Num, Gen int }

// Dict — словарь, << /Ключ значение >>.
type Dict map[Name]Object

// Array — массив, [ ... ].
type Array []Object

// String — строка, (текст) или <68656b>. Байты хранятся как есть: их смысл
// зависит от кодировки шрифта и раскрывается при извлечении текста.
type String []byte

// Object — любой объект PDF: nil, bool, int64, float64, Name, String, Ref,
// Dict, Array или *Stream.
type Object any

// Stream — поток: словарь и сырые данные до применения фильтров.
type Stream struct {
	Dict Dict
	Raw  []byte
}

// Get возвращает значение ключа без разрешения ссылок.
func (d Dict) Get(key Name) Object {
	if d == nil {
		return nil
	}
	return d[key]
}

// Пределы разбора. Они нужны не для красоты: повреждённый файл легко заставляет
// разборщик набивать массив миллионами элементов или уходить вглубь на тысячи
// уровней. Найдено обстрелом испорченных книг — разбор 33-мегабайтного файла
// не завершался за десять минут. У настоящих документов эти пределы недостижимы:
// самые длинные массивы (ширины глифов, списки страниц) — тысячи элементов,
// вложенность — единицы уровней.
const (
	maxArrayItems = 100000 // элементов в одном массиве
	maxDictKeys   = 8192   // ключей в одном словаре
	maxDepth      = 64     // глубина вложенности массивов и словарей
)

// errEndCollection сообщает, что встретился закрывающий разделитель.
var errEndCollection = errors.New("конец составного объекта")

// errEOF — данные кончились.
var errEOF = errors.New("неожиданный конец данных")

func isSpace(c byte) bool {
	return c == 0 || c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == ' '
}

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func isRegular(c byte) bool { return !isSpace(c) && !isDelim(c) }

// resolver разрешает ссылки; нужен парсеру только для /Length потока.
type resolver interface {
	Resolve(Object) Object
}

// parser разбирает поток байтов PDF на объекты. Он же используется для разбора
// содержимого страниц, где вперемешку идут объекты-операнды и операторы.
type parser struct {
	b     []byte
	pos   int
	res   resolver
	depth int // текущая вложенность составных объектов
}

func newParser(b []byte, res resolver) *parser { return &parser{b: b, res: res} }

func (p *parser) skipSpace() {
	for p.pos < len(p.b) {
		c := p.b[p.pos]
		switch {
		case isSpace(c):
			p.pos++
		case c == '%': // комментарий до конца строки
			for p.pos < len(p.b) && p.b[p.pos] != '\n' && p.b[p.pos] != '\r' {
				p.pos++
			}
		default:
			return
		}
	}
}

// token читает очередной элемент: либо объект, либо оператор (для содержимого
// страниц). Ровно один из obj/op осмыслен, что именно — говорит isOp.
func (p *parser) token() (obj Object, op string, isOp bool, err error) {
	p.skipSpace()
	if p.pos >= len(p.b) {
		return nil, "", false, errEOF
	}
	c := p.b[p.pos]
	switch {
	case c == '/':
		n, err := p.name()
		return n, "", false, err
	case c == '(':
		s, err := p.literalString()
		return s, "", false, err
	case c == '<':
		if p.pos+1 < len(p.b) && p.b[p.pos+1] == '<' {
			d, err := p.dict()
			if err != nil {
				return nil, "", false, err
			}
			return p.maybeStream(d)
		}
		s, err := p.hexString()
		return s, "", false, err
	case c == '[':
		a, err := p.array()
		return a, "", false, err
	case c == ']' || c == '>' || c == '}' || c == ')':
		p.pos++
		if c == '>' && p.pos < len(p.b) && p.b[p.pos] == '>' {
			p.pos++
		}
		return nil, "", false, errEndCollection
	case c == '{':
		p.pos++
		return nil, "", false, errEndCollection
	case c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9'):
		return p.number()
	default:
		kw := p.keyword()
		switch kw {
		case "true":
			return true, "", false, nil
		case "false":
			return false, "", false, nil
		case "null":
			return nil, "", false, nil
		case "":
			// Неразбираемый байт: пропускаем, чтобы не зациклиться.
			p.pos++
			return nil, "", false, errEndCollection
		}
		return nil, kw, true, nil
	}
}

// object читает объект, отвергая операторы.
func (p *parser) object() (Object, error) {
	obj, op, isOp, err := p.token()
	if err != nil {
		return nil, err
	}
	if isOp {
		return nil, fmt.Errorf("ожидался объект, встречен оператор %q", op)
	}
	return obj, nil
}

func (p *parser) keyword() string {
	start := p.pos
	for p.pos < len(p.b) && isRegular(p.b[p.pos]) {
		p.pos++
	}
	return string(p.b[start:p.pos])
}

func (p *parser) name() (Name, error) {
	p.pos++ // '/'
	var out []byte
	for p.pos < len(p.b) && isRegular(p.b[p.pos]) {
		c := p.b[p.pos]
		if c == '#' && p.pos+2 < len(p.b) {
			if v, err := strconv.ParseUint(string(p.b[p.pos+1:p.pos+3]), 16, 8); err == nil {
				out = append(out, byte(v))
				p.pos += 3
				continue
			}
		}
		out = append(out, c)
		p.pos++
	}
	return Name(out), nil
}

// number читает число, а для целого проверяет, не ссылка ли это: «12 0 R».
func (p *parser) number() (Object, string, bool, error) {
	start := p.pos
	if c := p.b[p.pos]; c == '+' || c == '-' {
		p.pos++
	}
	isReal := false
	for p.pos < len(p.b) {
		c := p.b[p.pos]
		if c >= '0' && c <= '9' {
			p.pos++
			continue
		}
		if c == '.' || c == 'e' || c == 'E' || c == '-' || c == '+' {
			// Второй знак может быть частью экспоненты или мусором вида «1-2».
			isReal = true
			p.pos++
			continue
		}
		break
	}
	text := string(p.b[start:p.pos])
	if !isReal {
		if v, err := strconv.ParseInt(text, 10, 64); err == nil {
			if ref, ok := p.tryRef(v); ok {
				return ref, "", false, nil
			}
			return v, "", false, nil
		}
	}
	v, err := parseFloat(text)
	if err != nil {
		return int64(0), "", false, nil // битое число не повод бросать весь разбор
	}
	return v, "", false, nil
}

// parseFloat разбирает число, прощая записи вида «--3» и «4.» из живых файлов.
func parseFloat(s string) (float64, error) {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v, nil
	}
	clean := make([]byte, 0, len(s))
	dot := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '-' || c == '+':
			if len(clean) == 0 {
				clean = append(clean, c)
			}
		case c == '.':
			if !dot {
				dot = true
				clean = append(clean, c)
			}
		case c >= '0' && c <= '9':
			clean = append(clean, c)
		}
	}
	if len(clean) == 0 {
		return 0, fmt.Errorf("не число: %q", s)
	}
	return strconv.ParseFloat(string(clean), 64)
}

// tryRef смотрит вперёд: если дальше идут поколение и «R», это ссылка.
func (p *parser) tryRef(num int64) (Ref, bool) {
	save := p.pos
	p.skipSpace()
	start := p.pos
	for p.pos < len(p.b) && p.b[p.pos] >= '0' && p.b[p.pos] <= '9' {
		p.pos++
	}
	if p.pos == start {
		p.pos = save
		return Ref{}, false
	}
	gen, err := strconv.Atoi(string(p.b[start:p.pos]))
	if err != nil {
		p.pos = save
		return Ref{}, false
	}
	p.skipSpace()
	if p.pos < len(p.b) && p.b[p.pos] == 'R' &&
		(p.pos+1 >= len(p.b) || !isRegular(p.b[p.pos+1])) {
		p.pos++
		return Ref{Num: int(num), Gen: gen}, true
	}
	p.pos = save
	return Ref{}, false
}

func (p *parser) literalString() (String, error) {
	p.pos++ // '('
	var out []byte
	depth := 1
	for p.pos < len(p.b) {
		c := p.b[p.pos]
		p.pos++
		switch c {
		case '\\':
			if p.pos >= len(p.b) {
				return out, nil
			}
			e := p.b[p.pos]
			p.pos++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '(', ')', '\\':
				out = append(out, e)
			case '\r': // перенос строки внутри строки: склеиваем
				if p.pos < len(p.b) && p.b[p.pos] == '\n' {
					p.pos++
				}
			case '\n':
			default:
				if e >= '0' && e <= '7' { // восьмеричный код
					v := int(e - '0')
					for i := 0; i < 2 && p.pos < len(p.b); i++ {
						d := p.b[p.pos]
						if d < '0' || d > '7' {
							break
						}
						v = v*8 + int(d-'0')
						p.pos++
					}
					out = append(out, byte(v))
				} else {
					out = append(out, e)
				}
			}
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return out, nil
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out, nil
}

func (p *parser) hexString() (String, error) {
	p.pos++ // '<'
	var out []byte
	var hi byte
	half := false
	for p.pos < len(p.b) {
		c := p.b[p.pos]
		p.pos++
		if c == '>' {
			break
		}
		v, ok := hexVal(c)
		if !ok {
			continue
		}
		if half {
			out = append(out, hi<<4|v)
			half = false
		} else {
			hi = v
			half = true
		}
	}
	if half { // нечётное число цифр: последняя дополняется нулём
		out = append(out, hi<<4)
	}
	return out, nil
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func (p *parser) array() (Array, error) {
	p.pos++ // '['
	if p.depth >= maxDepth {
		return nil, nil // слишком глубоко: дальше только мусор
	}
	p.depth++
	defer func() { p.depth-- }()

	var out Array
	for {
		obj, op, isOp, err := p.token()
		if errors.Is(err, errEndCollection) {
			return out, nil
		}
		if err != nil {
			return out, nil // не рушим разбор из-за обрыва
		}
		if isOp {
			// В массивах операторов быть не должно; пропускаем мусор.
			_ = op
			continue
		}
		if len(out) >= maxArrayItems {
			// Столько бывает только у испорченного файла: закрывающей скобки
			// нет, и мы читаем весь остаток документа как один массив.
			return out, nil
		}
		out = append(out, obj)
	}
}

func (p *parser) dict() (Dict, error) {
	p.pos += 2 // '<<'
	if p.depth >= maxDepth {
		return nil, nil
	}
	p.depth++
	defer func() { p.depth-- }()

	out := Dict{}
	for {
		if len(out) >= maxDictKeys {
			return out, nil
		}
		p.skipSpace()
		if p.pos >= len(p.b) {
			return out, nil
		}
		if p.b[p.pos] == '>' {
			p.pos++
			if p.pos < len(p.b) && p.b[p.pos] == '>' {
				p.pos++
			}
			return out, nil
		}
		if p.b[p.pos] != '/' {
			// Ключ обязан быть именем; если это не так, файл битый — пропускаем
			// один элемент и пробуем дальше.
			if _, _, _, err := p.token(); err != nil {
				return out, nil
			}
			continue
		}
		key, err := p.name()
		if err != nil {
			return out, nil
		}
		val, op, isOp, err := p.token()
		if errors.Is(err, errEndCollection) {
			return out, nil
		}
		if err != nil {
			return out, nil
		}
		if isOp {
			_ = op
			continue
		}
		out[key] = val
	}
}

// maybeStream проверяет, не начинается ли за словарём поток.
func (p *parser) maybeStream(d Dict) (Object, string, bool, error) {
	save := p.pos
	p.skipSpace()
	if p.pos+6 > len(p.b) || string(p.b[p.pos:p.pos+6]) != "stream" {
		p.pos = save
		return d, "", false, nil
	}
	p.pos += 6
	// После ключевого слова обязателен перевод строки.
	if p.pos < len(p.b) && p.b[p.pos] == '\r' {
		p.pos++
	}
	if p.pos < len(p.b) && p.b[p.pos] == '\n' {
		p.pos++
	}
	start := p.pos

	length := -1
	if p.res != nil {
		if v, ok := toInt(p.res.Resolve(d["Length"])); ok {
			length = v
		}
	} else if v, ok := toInt(d["Length"]); ok {
		length = v
	}

	end := -1
	if length >= 0 && start+length <= len(p.b) {
		// Доверяем /Length, только если сразу за данными действительно endstream.
		tail := p.b[start+length:]
		if k := indexKeyword(tail, "endstream", 4); k >= 0 && k <= 4 {
			end = start + length
		}
	}
	if end < 0 {
		k := indexKeyword(p.b[start:], "endstream", len(p.b))
		if k < 0 {
			end = len(p.b)
		} else {
			end = start + k
			// Отрезаем перевод строки, добавленный перед endstream.
			for end > start && (p.b[end-1] == '\n' || p.b[end-1] == '\r') {
				end--
			}
		}
	}
	raw := p.b[start:end]
	p.pos = end
	if k := indexKeyword(p.b[p.pos:], "endstream", len(p.b)); k >= 0 {
		p.pos += k + len("endstream")
	}
	return &Stream{Dict: d, Raw: raw}, "", false, nil
}

// indexKeyword ищет ключевое слово: сначала в пределах limit байт от начала,
// затем во всём остатке.
//
// Поиск идёт через bytes.Index намеренно. Наивный перебор байт здесь давал
// квадратичное время: у документа с испорченными длинами потоков конец потока
// приходится искать до конца файла, и так для каждого потока — на 33 МиБ разбор
// переставал завершаться вовсе.
func indexKeyword(b []byte, kw string, limit int) int {
	if limit > len(b) {
		limit = len(b)
	}
	if i := bytes.Index(b[:limit], []byte(kw)); i >= 0 {
		return i
	}
	if limit >= len(b) {
		return -1
	}
	if i := bytes.Index(b[limit:], []byte(kw)); i >= 0 {
		return limit + i
	}
	return -1
}

// toInt приводит числовой объект к int.
func toInt(o Object) (int, bool) {
	switch v := o.(type) {
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

// toFloat приводит числовой объект к float64.
func toFloat(o Object) (float64, bool) {
	switch v := o.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	}
	return 0, false
}
