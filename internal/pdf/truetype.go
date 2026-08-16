package pdf

import "encoding/binary"

// Встроенный шрифт как источник соответствия кодов символам.
//
// Составные шрифты с кодировкой Identity-H сплошь и рядом идут без таблицы
// /ToUnicode: так делает, например, Word, сохраняя подмножество Arial. Коды в
// содержимом страницы — это номера глифов внутри шрифта, и сами по себе они
// ничего не значат. Но в файле шрифта есть таблица cmap, которая переводит
// Unicode в номера глифов; развернув её наоборот, получаем то, что нужно.

// loadEmbeddedCmap достаёт соответствие «номер глифа → символ» из встроенного
// файла шрифта TrueType.
func (d *Document) loadEmbeddedCmap(dict Dict, f *font) {
	desc := d.dictOf(dict["FontDescriptor"])
	if desc == nil {
		if dfs := asArray(d.Resolve(dict["DescendantFonts"])); len(dfs) > 0 {
			desc = d.dictOf(d.dictOf(dfs[0])["FontDescriptor"])
		}
	}
	if desc == nil {
		return
	}
	s, ok := d.Resolve(desc["FontFile2"]).(*Stream)
	if !ok {
		return
	}
	data, err := d.Decode(s)
	if err != nil && len(data) == 0 {
		return
	}
	for gid, r := range parseTrueTypeCmap(data) {
		if _, exists := f.toUni[gid]; !exists {
			f.toUni[gid] = string(r)
		}
	}
}

// parseTrueTypeCmap возвращает соответствие «номер глифа → символ».
func parseTrueTypeCmap(data []byte) map[uint32]rune {
	out := map[uint32]rune{}
	if len(data) < 12 {
		return out
	}
	numTables := int(binary.BigEndian.Uint16(data[4:]))
	if 12+numTables*16 > len(data) {
		return out
	}
	var cmapOff, cmapLen int
	for i := 0; i < numTables; i++ {
		rec := data[12+i*16:]
		if string(rec[:4]) == "cmap" {
			cmapOff = int(binary.BigEndian.Uint32(rec[8:]))
			cmapLen = int(binary.BigEndian.Uint32(rec[12:]))
			break
		}
	}
	if cmapOff <= 0 || cmapOff+4 > len(data) {
		return out
	}
	if cmapLen <= 0 || cmapOff+cmapLen > len(data) {
		cmapLen = len(data) - cmapOff
	}
	cmap := data[cmapOff : cmapOff+cmapLen]
	if len(cmap) < 4 {
		return out
	}
	n := int(binary.BigEndian.Uint16(cmap[2:]))
	if 4+n*8 > len(cmap) {
		return out
	}
	// Выбираем подтаблицу: сначала Windows Unicode, затем что найдётся.
	best := -1
	bestRank := -1
	for i := 0; i < n; i++ {
		rec := cmap[4+i*8:]
		plat := binary.BigEndian.Uint16(rec)
		enc := binary.BigEndian.Uint16(rec[2:])
		off := int(binary.BigEndian.Uint32(rec[4:]))
		rank := 0
		switch {
		case plat == 3 && enc == 10:
			rank = 4
		case plat == 3 && enc == 1:
			rank = 3
		case plat == 0:
			rank = 2
		case plat == 3 && enc == 0: // символьная: коды лежат в области 0xF000
			rank = 1
		}
		if rank > bestRank && off > 0 && off < len(cmap) {
			bestRank, best = rank, off
		}
	}
	if best < 0 {
		return out
	}
	sub := cmap[best:]
	if len(sub) < 4 {
		return out
	}
	switch binary.BigEndian.Uint16(sub) {
	case 4:
		parseCmapFormat4(sub, out)
	case 12:
		parseCmapFormat12(sub, out)
	case 6:
		parseCmapFormat6(sub, out)
	}
	return out
}

// parseCmapFormat4 разбирает самый распространённый формат — сегменты BMP.
func parseCmapFormat4(sub []byte, out map[uint32]rune) {
	if len(sub) < 14 {
		return
	}
	segX2 := int(binary.BigEndian.Uint16(sub[6:]))
	seg := segX2 / 2
	if seg == 0 || 14+segX2*4+2 > len(sub) {
		return
	}
	endAt := 14
	startAt := endAt + segX2 + 2
	deltaAt := startAt + segX2
	rangeAt := deltaAt + segX2
	if rangeAt+segX2 > len(sub) {
		return
	}
	u16 := func(base, i int) uint16 {
		p := base + i*2
		if p+2 > len(sub) {
			return 0
		}
		return binary.BigEndian.Uint16(sub[p:])
	}
	for i := 0; i < seg; i++ {
		start, end := u16(startAt, i), u16(endAt, i)
		if start > end || end == 0xFFFF && start == 0xFFFF {
			continue
		}
		delta, ro := u16(deltaAt, i), u16(rangeAt, i)
		for c := uint32(start); c <= uint32(end); c++ {
			var gid uint16
			if ro == 0 {
				gid = uint16(c) + delta
			} else {
				p := rangeAt + i*2 + int(ro) + int(c-uint32(start))*2
				if p+2 > len(sub) {
					continue
				}
				gid = binary.BigEndian.Uint16(sub[p:])
				if gid != 0 {
					gid += delta
				}
			}
			addGlyph(out, uint32(gid), rune(c))
		}
	}
}

// parseCmapFormat12 разбирает формат с группами — он нужен для символов вне BMP.
func parseCmapFormat12(sub []byte, out map[uint32]rune) {
	if len(sub) < 16 {
		return
	}
	groups := int(binary.BigEndian.Uint32(sub[12:]))
	for i := 0; i < groups; i++ {
		p := 16 + i*12
		if p+12 > len(sub) {
			return
		}
		start := binary.BigEndian.Uint32(sub[p:])
		end := binary.BigEndian.Uint32(sub[p+4:])
		gid := binary.BigEndian.Uint32(sub[p+8:])
		if end < start || end-start > 65536 {
			continue
		}
		for c := start; c <= end; c++ {
			addGlyph(out, gid+(c-start), rune(c))
		}
	}
}

// parseCmapFormat6 разбирает непрерывный диапазон кодов.
func parseCmapFormat6(sub []byte, out map[uint32]rune) {
	if len(sub) < 10 {
		return
	}
	first := uint32(binary.BigEndian.Uint16(sub[6:]))
	count := int(binary.BigEndian.Uint16(sub[8:]))
	for i := 0; i < count; i++ {
		p := 10 + i*2
		if p+2 > len(sub) {
			return
		}
		addGlyph(out, uint32(binary.BigEndian.Uint16(sub[p:])), rune(first+uint32(i)))
	}
}

// addGlyph записывает соответствие, отдавая предпочтение меньшему коду:
// один глиф бывает привязан к нескольким символам, и обычный из них — младший.
func addGlyph(out map[uint32]rune, gid uint32, r rune) {
	if gid == 0 || r == 0 || r == 0xFFFF {
		return
	}
	// Символьные шрифты держат коды в служебной области 0xF000–0xF0FF.
	if r >= 0xF000 && r <= 0xF0FF {
		r -= 0xF000
	}
	if old, ok := out[gid]; ok && old <= r {
		return
	}
	out[gid] = r
}
