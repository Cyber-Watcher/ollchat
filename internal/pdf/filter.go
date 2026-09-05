package pdf

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
)

// errImageStream сообщает, что поток — картинка, а не текст.
var errImageStream = errors.New("поток содержит изображение")

// maxDecoded ограничивает размер распакованного потока: битый или намеренно
// раздутый файл не должен съесть всю память.
const maxDecoded = 256 << 20

// Decode применяет к потоку цепочку фильтров из /Filter и возвращает данные.
func (d *Document) Decode(s *Stream) ([]byte, error) {
	if s == nil {
		return nil, errors.New("пустой поток")
	}
	data := s.Raw
	// Расшифровка идёт до фильтров: в файле поток сначала сжат, потом
	// зашифрован, и разворачивать надо в обратном порядке.
	if d.crypt != nil && s.Dict["Type"] != Name("XRef") {
		data = d.crypt.decrypt(data)
	}
	filters := asArray(d.Resolve(s.Dict["Filter"]))
	parms := asArray(d.Resolve(s.Dict["DecodeParms"]))
	if len(parms) == 0 {
		parms = asArray(d.Resolve(s.Dict["DP"]))
	}

	for i, f := range filters {
		name, ok := d.Resolve(f).(Name)
		if !ok {
			continue
		}
		var parm Dict
		if i < len(parms) {
			parm, _ = d.Resolve(parms[i]).(Dict)
		}
		var err error
		data, err = d.applyFilter(name, data, parm)
		if err != nil {
			return data, err
		}
	}
	return data, nil
}

func (d *Document) applyFilter(name Name, data []byte, parm Dict) ([]byte, error) {
	switch name {
	case "FlateDecode", "Fl":
		out, err := inflate(data)
		if err != nil && len(out) == 0 {
			return nil, fmt.Errorf("FlateDecode: %w", err)
		}
		return d.predict(out, parm)
	case "LZWDecode", "LZW":
		early := 1
		if v, ok := toInt(d.Resolve(parm["EarlyChange"])); ok {
			early = v
		}
		out := lzwDecode(data, early == 1)
		return d.predict(out, parm)
	case "ASCIIHexDecode", "AHx":
		return asciiHexDecode(data), nil
	case "ASCII85Decode", "A85":
		return ascii85Decode(data), nil
	case "RunLengthDecode", "RL":
		return runLengthDecode(data), nil
	case "Crypt":
		return data, nil
	case "DCTDecode", "JPXDecode", "JBIG2Decode", "CCITTFaxDecode":
		return nil, errImageStream
	default:
		return data, nil
	}
}

// inflate распаковывает zlib или голый deflate, возвращая всё, что успело
// распаковаться: обрыв в конце потока — обычное дело в живых файлах.
func inflate(data []byte) ([]byte, error) {
	// Некоторые файлы оставляют мусор перед заголовком zlib.
	for i := 0; i < len(data) && i < 32; i++ {
		if !isSpace(data[i]) {
			data = data[i:]
			break
		}
	}
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return inflateRaw(data)
	}
	out, rerr := readAllLimited(r)
	if len(out) == 0 && rerr != nil {
		if raw, rawErr := inflateRaw(data); rawErr == nil || len(raw) > 0 {
			return raw, nil
		}
		return out, rerr
	}
	return out, nil
}

// inflateRaw распаковывает поток без заголовка zlib: попадаются и такие.
func inflateRaw(data []byte) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(data))
	defer r.Close()
	return readAllLimited(r)
}

func readAllLimited(r io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	_, err := io.Copy(&buf, io.LimitReader(r, maxDecoded))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return buf.Bytes(), err
	}
	return buf.Bytes(), nil
}

// predict снимает предиктор PNG или TIFF, применённый перед сжатием.
func (d *Document) predict(data []byte, parm Dict) ([]byte, error) {
	if parm == nil {
		return data, nil
	}
	pred, _ := toInt(d.Resolve(parm["Predictor"]))
	if pred <= 1 {
		return data, nil
	}
	colors := 1
	if v, ok := toInt(d.Resolve(parm["Colors"])); ok && v > 0 {
		colors = v
	}
	bpc := 8
	if v, ok := toInt(d.Resolve(parm["BitsPerComponent"])); ok && v > 0 {
		bpc = v
	}
	columns := 1
	if v, ok := toInt(d.Resolve(parm["Columns"])); ok && v > 0 {
		columns = v
	}
	bpp := (colors*bpc + 7) / 8 // байт на пиксель, не меньше одного
	rowLen := (colors*bpc*columns + 7) / 8

	if pred == 2 { // предиктор TIFF
		if bpc != 8 {
			return data, nil
		}
		for r := 0; r+rowLen <= len(data); r += rowLen {
			row := data[r : r+rowLen]
			for i := bpp; i < len(row); i++ {
				row[i] += row[i-bpp]
			}
		}
		return data, nil
	}

	// Предикторы PNG: каждая строка начинается с байта фильтра.
	out := make([]byte, 0, len(data))
	prev := make([]byte, rowLen)
	for pos := 0; pos+1 <= len(data); pos += rowLen + 1 {
		ft := data[pos]
		end := pos + 1 + rowLen
		if end > len(data) {
			end = len(data)
		}
		row := make([]byte, rowLen)
		copy(row, data[pos+1:end])
		switch ft {
		case 0:
		case 1: // Sub
			for i := bpp; i < rowLen; i++ {
				row[i] += row[i-bpp]
			}
		case 2: // Up
			for i := 0; i < rowLen; i++ {
				row[i] += prev[i]
			}
		case 3: // Average
			for i := 0; i < rowLen; i++ {
				var left byte
				if i >= bpp {
					left = row[i-bpp]
				}
				row[i] += byte((int(left) + int(prev[i])) / 2)
			}
		case 4: // Paeth
			for i := 0; i < rowLen; i++ {
				var left, upLeft byte
				if i >= bpp {
					left = row[i-bpp]
					upLeft = prev[i-bpp]
				}
				row[i] += paeth(left, prev[i], upLeft)
			}
		default:
			return out, nil
		}
		out = append(out, row...)
		prev = row
	}
	return out, nil
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := iabs(p-int(a)), iabs(p-int(b)), iabs(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	default:
		return c
	}
}

func iabs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// lzwDecode распаковывает вариант LZW из PDF. Стандартная библиотека Go не
// умеет «раннюю смену» ширины кода (EarlyChange), принятую в PDF по умолчанию,
// поэтому декодер написан здесь.
func lzwDecode(data []byte, early bool) []byte {
	const (
		clearCode = 256
		eodCode   = 257
	)
	var out []byte
	table := make([][]byte, 258, 4096)
	reset := func() {
		table = table[:258]
		for i := 0; i < 256; i++ {
			table[i] = []byte{byte(i)}
		}
	}
	reset()

	width := 9
	var bitBuf, bitCnt uint32
	pos := 0
	var prev []byte
	shift := 0
	if early {
		shift = 1
	}

	for {
		for bitCnt < uint32(width) {
			if pos >= len(data) {
				return out
			}
			bitBuf = bitBuf<<8 | uint32(data[pos])
			bitCnt += 8
			pos++
		}
		code := int(bitBuf >> (bitCnt - uint32(width)) & (1<<uint32(width) - 1))
		bitCnt -= uint32(width)

		switch {
		case code == eodCode:
			return out
		case code == clearCode:
			reset()
			width = 9
			prev = nil
			continue
		}

		var entry []byte
		switch {
		case code < len(table) && table[code] != nil:
			entry = table[code]
		case prev != nil:
			entry = append(append([]byte{}, prev...), prev[0])
		default:
			return out
		}
		out = append(out, entry...)
		if len(out) > maxDecoded {
			return out
		}
		if prev != nil && len(table) < 4096 {
			table = append(table, append(append([]byte{}, prev...), entry[0]))
		}
		prev = entry

		switch len(table) + shift {
		case 512:
			width = 10
		case 1024:
			width = 11
		case 2048:
			width = 12
		}
	}
}

func asciiHexDecode(data []byte) []byte {
	var out []byte
	var hi byte
	half := false
	for _, c := range data {
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
			hi, half = v, true
		}
	}
	if half {
		out = append(out, hi<<4)
	}
	return out
}

func ascii85Decode(data []byte) []byte {
	var out []byte
	var group [5]byte
	n := 0
	data = bytes.TrimPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte("<~"))
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case isSpace(c):
			continue
		case c == '~':
			i = len(data)
		case c == 'z' && n == 0:
			out = append(out, 0, 0, 0, 0)
			continue
		case c < '!' || c > 'u':
			continue
		default:
			group[n] = c - '!'
			n++
			if n == 5 {
				out = appendBase85(out, group, 5)
				n = 0
			}
			continue
		}
		break
	}
	if n > 1 {
		for i := n; i < 5; i++ {
			group[i] = 84
		}
		out = appendBase85(out, group, n)
	}
	return out
}

func appendBase85(out []byte, g [5]byte, n int) []byte {
	v := uint32(0)
	for _, b := range g {
		v = v*85 + uint32(b)
	}
	buf := [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	return append(out, buf[:n-1]...)
}

func runLengthDecode(data []byte) []byte {
	var out []byte
	for i := 0; i < len(data); {
		l := int(data[i])
		i++
		switch {
		case l == 128:
			return out
		case l < 128:
			end := i + l + 1
			if end > len(data) {
				end = len(data)
			}
			out = append(out, data[i:end]...)
			i = end
		default:
			if i >= len(data) {
				return out
			}
			for j := 0; j < 257-l; j++ {
				out = append(out, data[i])
			}
			i++
		}
	}
	return out
}

// asArray приводит объект к массиву: одиночное значение считается массивом из
// одного элемента, как того требует спецификация для /Filter.
func asArray(o Object) Array {
	switch v := o.(type) {
	case Array:
		return v
	case nil:
		return nil
	default:
		return Array{v}
	}
}
