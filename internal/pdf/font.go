package pdf

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// font — всё, что нужно, чтобы превратить байты строки в текст.
//
// Главный источник — таблица /ToUnicode: она есть почти у всех современных
// файлов и переводит внутренние коды глифов в Unicode. Без неё остаются
// встроенные кодировки (/WinAnsiEncoding и подобные) с поправками /Differences.
type font struct {
	twoByte  bool              // коды по два байта (Type0 с Identity-H)
	toUni    map[uint32]string // код глифа → строка Unicode
	simple   map[byte]rune     // однобайтовая кодировка
	widths   map[uint32]float64
	defWidth float64
	// Сплошные диапазоны хранятся диапазонами, а не поэлементно: у документов
	// с распознанным слоем встречаются шрифты с таблицей на все 65536 кодов,
	// и таких шрифтов в книге бывают тысячи — поэлементно это гигабайты.
	uniRanges   []uniRange
	widthRanges []widthRange
	// unknown — коды, для которых кодировка объявила имя глифа, но смысла у
	// него нет (например «a22» у шрифтов Type3). Подставлять вместо них
	// латиницу нельзя: получится правдоподобная бессмыслица.
	unknown map[byte]bool
}

// uniRange — сплошной участок таблицы соответствия.
type uniRange struct {
	lo, hi uint32
	base   []rune
}

// widthRange — сплошной участок таблицы ширин.
type widthRange struct {
	lo, hi uint32
	w      float64
}

// maxExpand — до какого размера диапазон разворачивается поэлементно.
// Порог низкий намеренно: таблицы распознанного слоя состоят из сотен
// диапазонов ровно по 256 кодов, и при большем пороге они разворачиваются
// целиком — тысяча таких шрифтов даёт полтора гигабайта на пустом месте.
const maxExpand = 16

// lookup ищет символы для кода: сначала в таблице, затем в диапазонах.
func (f *font) lookup(code uint32) (string, bool) {
	if s, ok := f.toUni[code]; ok {
		return s, true
	}
	if r, ok := findRange(f.uniRanges, code); ok {
		out := make([]rune, len(r.base))
		copy(out, r.base)
		out[len(out)-1] += rune(code - r.lo)
		return string(out), true
	}
	return "", false
}

// findRange ищет диапазон двоичным поиском: диапазонов бывают сотни, а поиск
// делается на каждый глиф страницы.
func findRange[T interface{ bounds() (uint32, uint32) }](ranges []T, code uint32) (T, bool) {
	var zero T
	lo, hi := 0, len(ranges)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		a, b := ranges[mid].bounds()
		switch {
		case code < a:
			hi = mid - 1
		case code > b:
			lo = mid + 1
		default:
			return ranges[mid], true
		}
	}
	return zero, false
}

func (r uniRange) bounds() (uint32, uint32)   { return r.lo, r.hi }
func (r widthRange) bounds() (uint32, uint32) { return r.lo, r.hi }

// shown — результат разбора строки: текст и то, сколько места он занял.
type shown struct {
	text     string
	unmapped int     // сколько кодов не удалось сопоставить
	width    float64 // ширина в тысячных долях кегля
	glyphs   int
	spaces   int // однобайтовые пробелы, к ним применяется словный интервал
}

// width возвращает ширину глифа в тысячных долях кегля.
func (f *font) widthOf(code uint32) float64 {
	if w, ok := f.widths[code]; ok {
		return w
	}
	if r, ok := findRange(f.widthRanges, code); ok {
		return r.w
	}
	if f.defWidth > 0 {
		return f.defWidth
	}
	if f.twoByte {
		return 1000
	}
	return 500
}

// decode переводит строку из содержимого страницы в текст и считает, насколько
// сдвинется перо: без ширины глифов не отличить конец слова от простого сдвига,
// и текст рассыпается на буквы через пробел.
func (f *font) decode(b []byte) shown {
	var sb strings.Builder
	var out shown
	if f == nil {
		// Шрифт не объявлен: считаем однобайтовой латиницей.
		for _, c := range b {
			sb.WriteRune(winAnsi[c])
			out.width += 500
			out.glyphs++
		}
		out.text = sb.String()
		return out
	}
	if f.twoByte {
		for i := 0; i+1 < len(b); i += 2 {
			code := uint32(b[i])<<8 | uint32(b[i+1])
			out.width += f.widthOf(code)
			out.glyphs++
			if s, ok := f.lookup(code); ok {
				sb.WriteString(s)
				continue
			}
			out.unmapped++
		}
		out.text = sb.String()
		return out
	}
	for _, c := range b {
		code := uint32(c)
		out.width += f.widthOf(code)
		out.glyphs++
		if c == ' ' {
			out.spaces++
		}
		if s, ok := f.lookup(code); ok {
			sb.WriteString(s)
			continue
		}
		// Поправка /Differences перекрывает базовую кодировку — в том числе
		// когда имя глифа бессмысленно. Подставить сюда букву из WinAnsi
		// значит выдать связную с виду бессмыслицу вместо честного отказа.
		if f.unknown[c] {
			out.unmapped++
			continue
		}
		if r, ok := f.simple[c]; ok && r != 0 {
			sb.WriteRune(r)
			continue
		}
		if r := winAnsi[c]; r != 0 {
			sb.WriteRune(r)
			continue
		}
		out.unmapped++
	}
	out.text = sb.String()
	return out
}

// loadFont собирает описание шрифта из его словаря.
func (d *Document) loadFont(dict Dict) *font {
	if dict == nil {
		return nil
	}
	f := &font{
		toUni:   map[uint32]string{},
		simple:  map[byte]rune{},
		widths:  map[uint32]float64{},
		unknown: map[byte]bool{},
	}

	// Ширина кода определяется типом шрифта, и только им. Таблица /ToUnicode
	// часто объявляет диапазон <0000>–<FFFF> даже у однобайтовых шрифтов —
	// верить ей нельзя, иначе простой шрифт читается по два байта и даёт мусор.
	subtype, _ := d.Resolve(dict["Subtype"]).(Name)
	if subtype == "Type0" {
		f.twoByte = true // Identity-H и прочие двухбайтовые CMap
		d.loadCIDWidths(dict, f)
	} else {
		d.loadSimpleWidths(dict, f)
	}

	if s, ok := d.Resolve(dict["ToUnicode"]).(*Stream); ok {
		if data, err := d.Decode(s); err == nil || len(data) > 0 {
			d.parseCMap(data, f)
		}
	}
	if len(f.toUni) == 0 && len(f.uniRanges) == 0 && f.twoByte {
		// Таблицы соответствия нет — достаём её из самого встроенного шрифта.
		d.loadEmbeddedCmap(dict, f)
	}

	// Однобайтовые шрифты: базовая кодировка плюс поправки.
	switch enc := d.Resolve(dict["Encoding"]).(type) {
	case Name:
		f.applyBaseEncoding(enc)
	case Dict:
		if base, ok := d.Resolve(enc["BaseEncoding"]).(Name); ok {
			f.applyBaseEncoding(base)
		} else {
			f.applyBaseEncoding("StandardEncoding")
		}
		code := 0
		for _, item := range asArray(d.Resolve(enc["Differences"])) {
			switch v := d.Resolve(item).(type) {
			case int64:
				code = int(v)
			case float64:
				code = int(v)
			case Name:
				if code >= 0 && code < 256 {
					if r := glyphRune(string(v)); r != 0 {
						f.simple[byte(code)] = r
					} else {
						f.unknown[byte(code)] = true
					}
				}
				code++
			}
		}
	default:
		if !f.twoByte {
			f.applyBaseEncoding("StandardEncoding")
		}
	}
	sort.Slice(f.uniRanges, func(i, j int) bool { return f.uniRanges[i].lo < f.uniRanges[j].lo })
	sort.Slice(f.widthRanges, func(i, j int) bool { return f.widthRanges[i].lo < f.widthRanges[j].lo })
	return f
}

// loadSimpleWidths читает /Widths однобайтового шрифта: массив идёт подряд,
// начиная с кода /FirstChar.
func (d *Document) loadSimpleWidths(dict Dict, f *font) {
	first, _ := toInt(d.Resolve(dict["FirstChar"]))
	widths := asArray(d.Resolve(dict["Widths"]))
	for i, item := range widths {
		if w, ok := toFloat(d.Resolve(item)); ok && w > 0 {
			f.widths[uint32(first+i)] = w
		}
	}
	if desc := d.dictOf(dict["FontDescriptor"]); desc != nil {
		if w, ok := toFloat(d.Resolve(desc["MissingWidth"])); ok && w > 0 {
			f.defWidth = w
		}
	}
}

// loadCIDWidths читает /W составного шрифта. Формат допускает две записи:
// «код [w1 w2 …]» и «первый последний ширина».
func (d *Document) loadCIDWidths(dict Dict, f *font) {
	descendants := asArray(d.Resolve(dict["DescendantFonts"]))
	if len(descendants) == 0 {
		return
	}
	cid := d.dictOf(descendants[0])
	if cid == nil {
		return
	}
	f.defWidth = 1000
	if w, ok := toFloat(d.Resolve(cid["DW"])); ok && w > 0 {
		f.defWidth = w
	}
	w := asArray(d.Resolve(cid["W"]))
	for i := 0; i < len(w); {
		start, ok := toInt(d.Resolve(w[i]))
		if !ok {
			i++
			continue
		}
		if i+1 >= len(w) {
			break
		}
		switch next := d.Resolve(w[i+1]).(type) {
		case Array:
			for j, item := range next {
				if v, ok := toFloat(d.Resolve(item)); ok {
					f.widths[uint32(start+j)] = v
				}
			}
			i += 2
		default:
			end, ok1 := toInt(next)
			if !ok1 || i+2 >= len(w) {
				i += 2
				continue
			}
			v, ok2 := toFloat(d.Resolve(w[i+2]))
			if ok2 && end >= start && end-start < 65536 {
				if end-start >= maxExpand {
					f.widthRanges = append(f.widthRanges, widthRange{uint32(start), uint32(end), v})
				} else {
					for c := start; c <= end; c++ {
						f.widths[uint32(c)] = v
					}
				}
			}
			i += 3
		}
	}
}

func (f *font) applyBaseEncoding(name Name) {
	var table *[256]rune
	switch name {
	case "WinAnsiEncoding":
		table = &winAnsi
	case "MacRomanEncoding":
		table = &macRoman
	default:
		table = &winAnsi // StandardEncoding для латиницы почти совпадает
	}
	for i := 0; i < 256; i++ {
		if r := table[i]; r != 0 {
			if _, ok := f.simple[byte(i)]; !ok {
				f.simple[byte(i)] = r
			}
		}
	}
}

// parseCMap разбирает таблицу /ToUnicode: разделы bfchar и bfrange.
func (d *Document) parseCMap(data []byte, f *font) {
	p := newParser(data, nil)
	var operands []Object
	for {
		obj, op, isOp, err := p.token()
		if err != nil {
			if err == errEOF {
				return
			}
			operands = operands[:0]
			continue
		}
		if !isOp {
			if len(operands) < 1024 {
				operands = append(operands, obj)
			}
			continue
		}
		switch op {
		case "endbfchar":
			for i := 0; i+1 < len(operands); i += 2 {
				src, ok1 := operands[i].(String)
				dst, ok2 := operands[i+1].(String)
				if !ok1 || !ok2 {
					continue
				}
				f.toUni[codeOf(src)] = utf16BE(dst)
			}
		case "endbfrange":
			for i := 0; i+2 < len(operands); i += 3 {
				lo, ok1 := operands[i].(String)
				hi, ok2 := operands[i+1].(String)
				if !ok1 || !ok2 {
					continue
				}
				start, end := codeOf(lo), codeOf(hi)
				if end < start || end-start > 65535 {
					continue
				}
				switch dst := operands[i+2].(type) {
				case String:
					// Диапазон отображается подряд, наращивается последний код.
					base := utf16Runes(dst)
					if len(base) == 0 {
						continue
					}
					if end-start >= maxExpand {
						f.uniRanges = append(f.uniRanges, uniRange{start, end, base})
						continue
					}
					for c := start; c <= end; c++ {
						r := make([]rune, len(base))
						copy(r, base)
						r[len(r)-1] += rune(c - start)
						f.toUni[c] = string(r)
					}
				case Array:
					for j, item := range dst {
						s, ok := item.(String)
						if !ok || start+uint32(j) > end {
							continue
						}
						f.toUni[start+uint32(j)] = utf16BE(s)
					}
				}
			}
		}
		operands = operands[:0]
	}
}

// codeOf собирает код глифа из байтов строки.
func codeOf(s String) uint32 {
	var v uint32
	for _, c := range s {
		v = v<<8 | uint32(c)
	}
	return v
}

// utf16BE переводит строку UTF-16BE, как её хранит /ToUnicode, в UTF-8.
func utf16BE(s String) string { return string(utf16Runes(s)) }

func utf16Runes(s String) []rune {
	if len(s) == 1 {
		return []rune{rune(s[0])}
	}
	units := make([]uint16, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		units = append(units, uint16(s[i])<<8|uint16(s[i+1]))
	}
	return utf16.Decode(units)
}

// glyphRune переводит имя глифа в символ. Поддержаны записи uniXXXX и uXXXX,
// имена кириллицы afiiNNNNN и набор латинских имён из встроенных кодировок.
func glyphRune(name string) rune {
	if name == "" || name == ".notdef" {
		return 0
	}
	if r, ok := glyphNames[name]; ok {
		return r
	}
	if strings.HasPrefix(name, "uni") && len(name) >= 7 {
		if v, err := strconv.ParseUint(name[3:7], 16, 32); err == nil {
			return rune(v)
		}
	}
	if strings.HasPrefix(name, "u") && len(name) >= 5 && len(name) <= 7 {
		if v, err := strconv.ParseUint(name[1:], 16, 32); err == nil {
			return rune(v)
		}
	}
	if strings.HasPrefix(name, "afii") {
		if v, err := strconv.Atoi(name[4:]); err == nil {
			if r, ok := afiiCyrillic[v]; ok {
				return r
			}
		}
	}
	// Имена вида «g123» или «cid123» осмысленного символа не несут.
	return 0
}

// afiiCyrillic — кириллица в старых именах глифов Adobe.
var afiiCyrillic = func() map[int]rune {
	m := map[int]rune{
		10023: 'Ё', 10071: 'ё',
		10050: 'Ґ', 10098: 'ґ',
		10051: 'Ђ', 10099: 'ђ',
		10052: 'Ѓ', 10100: 'ѓ',
		10053: 'Є', 10101: 'є',
		10054: 'Ѕ', 10102: 'ѕ',
		10055: 'І', 10103: 'і',
		10056: 'Ї', 10104: 'ї',
		10057: 'Ј', 10105: 'ј',
		10058: 'Љ', 10106: 'љ',
		10059: 'Њ', 10107: 'њ',
		10060: 'Ћ', 10108: 'ћ',
		10061: 'Ќ', 10109: 'ќ',
		10062: 'Ў', 10110: 'ў',
		10145: 'Џ', 10193: 'џ',
	}
	// Прописные А…Я идут подряд, кроме выпавшей Ё.
	upper := []rune("АБВГДЕЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯ")
	code := 10017
	for _, r := range upper {
		m[code] = r
		code++
		if code == 10023 { // здесь в нумерации стоит Ё
			code++
		}
	}
	lower := []rune("абвгдежзийклмнопрстуфхцчшщъыьэюя")
	code = 10065
	for _, r := range lower {
		m[code] = r
		code++
		if code == 10071 { // ё
			code++
		}
	}
	return m
}()

// glyphNames — имена глифов из встроенных кодировок PDF.
var glyphNames = map[string]rune{
	"space": ' ', "exclam": '!', "quotedbl": '"', "numbersign": '#', "dollar": '$',
	"percent": '%', "ampersand": '&', "quotesingle": '\'', "quoteright": '’',
	"quoteleft": '‘', "parenleft": '(', "parenright": ')', "asterisk": '*',
	"plus": '+', "comma": ',', "hyphen": '-', "period": '.', "slash": '/',
	"zero": '0', "one": '1', "two": '2', "three": '3', "four": '4', "five": '5',
	"six": '6', "seven": '7', "eight": '8', "nine": '9', "colon": ':',
	"semicolon": ';', "less": '<', "equal": '=', "greater": '>', "question": '?',
	"at": '@', "bracketleft": '[', "backslash": '\\', "bracketright": ']',
	"asciicircum": '^', "underscore": '_', "grave": '`', "braceleft": '{',
	"bar": '|', "braceright": '}', "asciitilde": '~',
	"quotedblleft": '“', "quotedblright": '”', "quotedblbase": '„',
	"quotesinglbase": '‚', "guillemotleft": '«', "guillemotright": '»',
	"guilsinglleft": '‹', "guilsinglright": '›',
	"endash": '–', "emdash": '—', "bullet": '•',
	"ellipsis": '…', "dagger": '†', "daggerdbl": '‡',
	"perthousand": '‰', "trademark": '™', "copyright": '©',
	"registered": '®', "degree": '°', "plusminus": '±',
	"paragraph": '¶', "section": '§', "sterling": '£',
	"euro": '€', "yen": '¥', "cent": '¢', "currency": '¤',
	"fi": 'ﬁ', "fl": 'ﬂ', "germandbls": 'ß',
	"adieresis": 'ä', "odieresis": 'ö', "udieresis": 'ü',
	"Adieresis": 'Ä', "Odieresis": 'Ö', "Udieresis": 'Ü',
	"eacute": 'é', "egrave": 'è', "ecircumflex": 'ê',
	"agrave": 'à', "aacute": 'á', "acircumflex": 'â',
	"ccedilla": 'ç', "ntilde": 'ñ', "oacute": 'ó',
	"uacute": 'ú', "iacute": 'í', "multiply": '×',
	"divide": '÷', "minus": '−', "fraction": '⁄',
}

// winAnsi — кодировка WinAnsiEncoding, она же cp1252.
var winAnsi = func() [256]rune {
	var t [256]rune
	for i := 32; i < 127; i++ {
		t[i] = rune(i)
	}
	high := map[int]rune{
		0x80: '€', 0x82: '‚', 0x83: 'ƒ', 0x84: '„',
		0x85: '…', 0x86: '†', 0x87: '‡', 0x88: 'ˆ',
		0x89: '‰', 0x8a: 'Š', 0x8b: '‹', 0x8c: 'Œ',
		0x8e: 'Ž', 0x91: '‘', 0x92: '’', 0x93: '“',
		0x94: '”', 0x95: '•', 0x96: '–', 0x97: '—',
		0x98: '˜', 0x99: '™', 0x9a: 'š', 0x9b: '›',
		0x9c: 'œ', 0x9e: 'ž', 0x9f: 'Ÿ',
	}
	for k, v := range high {
		t[k] = v
	}
	for i := 0xa0; i < 256; i++ {
		t[i] = rune(i) // верхняя половина совпадает с latin-1
	}
	t[0xad] = '­'
	return t
}()

// macRoman — кодировка MacRomanEncoding.
var macRoman = func() [256]rune {
	var t [256]rune
	for i := 32; i < 127; i++ {
		t[i] = rune(i)
	}
	high := []rune(
		"ÄÅÇÉÑÖÜáàâäãåçéèêëíìîïñóòôöõúùûü" +
			"†°¢£§•¶ß®©™´¨≠ÆØ∞±≤≥¥µ∂∑∏π∫ªºΩæø" +
			"¿¡¬√ƒ≈∆«»… ÀÃÕŒœ–—“”‘’÷◊ÿŸ⁄€‹›ﬁﬂ" +
			"‡·‚„‰ÂÊÁËÈÍÎÏÌÓÔÒÚÛÙıˆ˜¯˘˙˚¸˝˛ˇ")
	for i, r := range high {
		if 128+i < 256 {
			t[128+i] = r
		}
	}
	return t
}()
