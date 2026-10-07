package pdf

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Пределы разбора на нарочно составленных файлах. Каждый случай — файл
// в сотни байт, который до правки ронял процесс нехваткой памяти (её recover
// не ловит) или вешал разбор навсегда. Проверки ограничены по времени:
// зависание обязано провалить один тест, а не повесить весь прогон.

// bounded выполняет f не дольше d.
func bounded(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("разбор не уложился в %v — работа снова не ограничена", d)
	}
}

// zlibBytes сжимает данные, как это делает FlateDecode.
func zlibBytes(data []byte) []byte {
	var b bytes.Buffer
	zw := zlib.NewWriter(&b)
	zw.Write(data)
	zw.Close()
	return b.Bytes()
}

// objStmDoc — документ, у которого каталог и дерево страниц лежат в объектном
// потоке с заданным /N.
func objStmDoc(n string) []byte {
	inner := "<< /Type /Catalog /Pages 2 0 R >> << /Type /Pages /Kids [3 0 R] /Count 1 >>"
	head := "1 0 2 34 "
	packed := zlibBytes([]byte(head + inner))

	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	fmt.Fprintf(&b, "4 0 obj\n<< /Type /ObjStm /N %s /First %d /Filter /FlateDecode /Length %d >>\nstream\n",
		n, len(head), len(packed))
	b.Write(packed)
	b.WriteString("\nendstream\nendobj\n")
	b.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 6 0 R >>\nendobj\n")
	b.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")
	fmt.Fprintf(&b, "6 0 obj\n%s\nendobj\n", stream("", "BT /F1 12 Tf 10 700 Td (compressed) Tj ET"))
	b.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return b.Bytes()
}

// Огромный /Colors переполнял длину строки предиктора до нуля, и цикл по
// строкам TIFF стоял на месте вечно; огромный /Columns просил под строку
// терабайт. Обычный предиктор при этом работает как прежде.
func TestPredictorHostileParams(t *testing.T) {
	d := &Document{}
	bounded(t, 5*time.Second, func() {
		if _, err := d.predict([]byte{1, 2, 3, 4}, Dict{"Predictor": int64(2), "Colors": int64(1 << 61)}); err == nil {
			t.Error("TIFF с /Colors 2^61: ожидалась ошибка параметров")
		}
		out, err := d.predict([]byte{2, 1, 2}, Dict{"Predictor": int64(12), "Columns": int64(1 << 40)})
		if err == nil && len(out) > 3 {
			t.Errorf("строка на 2^40 столбцов из трёх байт дала %d байт", len(out))
		}
		if _, err := d.predict([]byte{0, 1}, Dict{"Predictor": int64(12), "BitsPerComponent": int64(64)}); err == nil {
			t.Error("BitsPerComponent 64: ожидалась ошибка параметров")
		}
	})
	// Обрезанный поток: одна неполная строка раскрывается как есть.
	out, err := d.predict([]byte{1, 5, 1}, Dict{"Predictor": int64(11), "Columns": int64(100)})
	if err != nil || !bytes.Equal(out, []byte{5, 6}) {
		t.Errorf("неполная строка: %v, %v", out, err)
	}
}

// /W с началом −2⁶³: разность end−start переполнялась, проходила проверку
// длины, и цикл по ширинам шёл около 2⁶³ витков.
func TestCIDWidthsOverflow(t *testing.T) {
	doc := docWith("/F1 5 0 R", "BT /F1 12 Tf 72 720 Td <0003> Tj ET",
		"<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H "+
			"/DescendantFonts [6 0 R] /ToUnicode 7 0 R >>",
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X "+
			"/W [-9223372036854775808 0 500 0 9223372036854775807 600 1 [700 800]] >>",
		stream("", "begincmap\n1 beginbfchar\n<0003> <0414>\nendbfchar\nendcmap"))
	bounded(t, 5*time.Second, func() {
		if got := pageText(t, doc); got != "Д" {
			t.Errorf("получено %q", got)
		}
	})
}

// pageText — текст первой страницы. В отличие от extract годится внутри
// bounded: ошибку отмечает, но горутину теста не останавливает.
func pageText(t *testing.T, data []byte) string {
	t.Helper()
	res, err := Extract(data, Options{})
	if err != nil {
		t.Errorf("извлечение: %v", err)
		return ""
	}
	return res.Pages[0].Text
}

// Диапазон /ToUnicode, кончающийся на <FFFFFFFF>: счётчик uint32
// переполнялся, цикл не кончался и набивал таблицу до нехватки памяти.
// Строка назначения длиннее 512 байт обрезается, как велит спецификация.
func TestToUnicodeRangeAtTop(t *testing.T) {
	long := "<" + strings.Repeat("0041", 4000) + ">"
	doc := docWith("/F1 5 0 R", "BT /F1 12 Tf 72 720 Td <00030004> Tj ET",
		"<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /ToUnicode 6 0 R >>",
		stream("", "begincmap\n1 beginbfrange\n<FFFFFFF0> <FFFFFFFF> <0041>\nendbfrange\n"+
			"2 beginbfchar\n<0003> <0414>\n<0004> "+long+"\nendbfchar\nendcmap"))
	bounded(t, 5*time.Second, func() {
		got := pageText(t, doc)
		if !strings.HasPrefix(got, "Д") || len(got) != len("Д")+maxUniDst/2 {
			t.Errorf("получено %d байт: %.40q…", len(got), got)
		}
	})
}

// ttfWithCmap собирает файл шрифта из одной таблицы cmap с одной подтаблицей.
func ttfWithCmap(sub []byte) []byte {
	be := binary.BigEndian
	cmap := make([]byte, 12, 12+len(sub))
	be.PutUint16(cmap[2:], 1)   // одна подтаблица
	be.PutUint16(cmap[4:], 3)   // Windows
	be.PutUint16(cmap[6:], 10)  // Unicode полный
	be.PutUint32(cmap[8:], 12)  // смещение подтаблицы
	cmap = append(cmap, sub...) //
	font := make([]byte, 28, 28+len(cmap))
	be.PutUint32(font[0:], 0x00010000)
	be.PutUint16(font[4:], 1) // одна таблица
	copy(font[12:], "cmap")
	be.PutUint32(font[20:], 28)
	be.PutUint32(font[24:], uint32(len(cmap)))
	return append(font, cmap...)
}

// cmapFormat12 — подтаблица формата 12 из групп {начало, конец, первый глиф}.
func cmapFormat12(groups ...[3]uint32) []byte {
	be := binary.BigEndian
	sub := make([]byte, 16+12*len(groups))
	be.PutUint16(sub[0:], 12)
	be.PutUint32(sub[4:], uint32(len(sub)))
	be.PutUint32(sub[12:], uint32(len(groups)))
	for i, g := range groups {
		be.PutUint32(sub[16+12*i:], g[0])
		be.PutUint32(sub[20+12*i:], g[1])
		be.PutUint32(sub[24+12*i:], g[2])
	}
	return sub
}

// Таблица cmap из встроенного шрифта: формат 12 в PDF на 2 КБ давал
// 26 миллионов записей, группа у верхней границы uint32 зацикливала счёт,
// а пересекающиеся сегменты формата 4 давали 2³¹ витков. Обычная таблица
// разбирается как прежде.
func TestTrueTypeCmapBounded(t *testing.T) {
	var groups [][3]uint32
	for i := uint32(0); i < 400; i++ {
		groups = append(groups, [3]uint32{i * 0x10000, i*0x10000 + 0xFFFF, i * 0x10000})
	}
	groups = append(groups, [3]uint32{0xFFFF0000, 0xFFFFFFFF, 1})
	hostile := ttfWithCmap(cmapFormat12(groups...))

	// Формат 4: 2000 сегментов во всю ширину BMP, idDelta = 1.
	const seg = 2000
	be := binary.BigEndian
	f4 := make([]byte, 16+seg*8)
	be.PutUint16(f4[0:], 4)
	be.PutUint16(f4[6:], seg*2)
	for i := 0; i < seg; i++ {
		be.PutUint16(f4[14+i*2:], 0xFFFE)          // конец сегмента
		be.PutUint16(f4[16+seg*2+i*2:], 1)         // начало
		be.PutUint16(f4[16+seg*4+i*2:], uint16(i)) // сдвиг номера глифа
	}
	overlapping := ttfWithCmap(f4)

	bounded(t, 10*time.Second, func() {
		if n := len(parseTrueTypeCmap(hostile)); n > maxGlyphID {
			t.Errorf("формат 12: записей %d при глифах до %d", n, maxGlyphID)
		}
		if n := len(parseTrueTypeCmap(overlapping)); n > maxGlyphID {
			t.Errorf("формат 4: записей %d", n)
		}
	})

	// Обычная группа: коды A–Z на глифы 3–28.
	normal := parseTrueTypeCmap(ttfWithCmap(cmapFormat12([3]uint32{'A', 'Z', 3})))
	if len(normal) != 26 || normal[3] != 'A' || normal[28] != 'Z' {
		t.Errorf("обычная таблица разобрана неверно: %d записей, %q %q", len(normal), normal[3], normal[28])
	}
}

// /N объектного потока берётся из файла: до правки под него заранее
// выделялась память — полтора терабайта на файл в килобайт.
func TestObjectStreamHugeCount(t *testing.T) {
	bounded(t, 10*time.Second, func() {
		res, err := Extract(objStmDoc("99999999999"), Options{})
		if err != nil {
			t.Errorf("извлечение: %v", err)
			return
		}
		if got := res.Pages[0].Text; got != "compressed" {
			t.Errorf("получено %q", got)
		}
	})
}
