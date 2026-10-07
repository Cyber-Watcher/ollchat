package pdf

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
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

// withBudget временно уменьшает пределы разбора: проверять их на настоящих
// величинах значило бы гонять гигабайты в каждом прогоне.
func withBudget(t *testing.T, stream int, base int64) {
	t.Helper()
	oldStream, oldBase, oldPer := maxDecoded, workBase, workPerByte
	maxDecoded, workBase, workPerByte = stream, base, 1
	t.Cleanup(func() { maxDecoded, workBase, workPerByte = oldStream, oldBase, oldPer })
}

// Каждый шаг цепочки фильтров ограничен: RunLength и ASCII85 тоже.
func TestFilterOutputLimited(t *testing.T) {
	rl := bytes.Repeat([]byte{129, 'x'}, 1000) // по 128 байт на пару
	if got := runLengthDecode(rl, 1000); len(got) != 1000 {
		t.Errorf("RunLength выдал %d байт при пределе 1000", len(got))
	}
	if got := ascii85Decode(bytes.Repeat([]byte("z"), 1000), 100); len(got) != 100 {
		t.Errorf("ASCII85 выдал %d байт при пределе 100", len(got))
	}
	if got := lzwDecode(lzwBomb(), true, 100); len(got) > 100 {
		t.Errorf("LZW выдал %d байт при пределе 100", len(got))
	}
}

// lzwBomb — коды LZW, раз за разом удлиняющие одну и ту же цепочку.
func lzwBomb() []byte {
	var codes []int
	codes = append(codes, 'a')
	for c := 258; c < 4000; c++ {
		codes = append(codes, c)
	}
	var out []byte
	var acc uint32
	var bits uint
	width := uint(9)
	for i, c := range codes {
		acc = acc<<width | uint32(c)
		bits += width
		for bits >= 8 {
			out = append(out, byte(acc>>(bits-8)))
			bits -= 8
		}
		switch i + 258 + 1 {
		case 511:
			width = 10
		case 1023:
			width = 11
		case 2047:
			width = 12
		}
	}
	return out
}

// Цепочка Flate → RunLength: Flate сам по себе ограничен, а RunLength после
// него разворачивал каждые два байта в 128 — поток в 64 КБ давал гигабайты.
// Бюджет документа обрывает такой файл ошибкой, а не нехваткой памяти.
func TestFilterChainBudget(t *testing.T) {
	withBudget(t, 64<<20, 32<<20)
	rl := bytes.Repeat([]byte{129, 'x'}, 4<<20) // 8 МБ → 512 МБ после RunLength
	packed := zlibBytes(rl)
	content := fmt.Sprintf("<< /Filter [/FlateDecode /RunLengthDecode] /Length %d >>\nstream\n%s\nendstream",
		len(packed), packed)
	doc := build(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << >> /Contents [4 0 R 4 0 R 4 0 R 4 0 R] >>",
		content)
	bounded(t, 20*time.Second, func() {
		if _, err := Extract(doc, Options{}); !errors.Is(err, ErrTooHeavy) {
			t.Errorf("ожидался ErrTooHeavy, получено %v", err)
		}
	})
	// Обычный документ в тот же бюджет укладывается.
	if got := pageText(t, docWith("/F1 5 0 R", "BT /F1 12 Tf 72 720 Td (Hello) Tj ET", helvetica)); got != "Hello" {
		t.Errorf("обычный документ: %q", got)
	}
}

// Массив /Contents из сотни ссылок на один большой поток: до правки они
// склеивались целиком, и страница требовала в сто раз больше предела потока.
func TestContentsArrayBounded(t *testing.T) {
	withBudget(t, 1<<20, 1<<40)
	packed := zlibBytes(bytes.Repeat([]byte("q Q "), 256<<10)) // ровно 1 МБ
	refs := strings.Repeat("4 0 R ", 100)
	doc := build(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << >> /Contents ["+refs+"] >>",
		fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(packed), packed))
	d, err := Open(doc)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(d.contentOf(d.Pages()[0])); n > maxDecoded+1 {
		t.Errorf("содержимое страницы %d байт при пределе %d", n, maxDecoded)
	}
}

// Форма, вызывающая саму себя десять раз: при глубине 8 это сто миллионов
// вызовов, файл в полкилобайта разбирался дольше полутора минут. Так же —
// цепочка из восьми разных форм, где каждая зовёт следующую десять раз.
func TestFormFanOutBounded(t *testing.T) {
	self := build(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Fm0 5 0 R >> /Font << /F1 6 0 R >> >> /Contents 4 0 R >>",
		stream("", "/Fm0 Do"),
		stream("<< /Type /XObject /Subtype /Form /Resources << /XObject << /Fm0 5 0 R >> /Font << /F1 6 0 R >> >> >>",
			"BT /F1 12 Tf 72 720 Td (loop) Tj ET "+strings.Repeat("/Fm0 Do ", 10)),
		helvetica)

	// Цепочка: объекты 5…12 — формы, каждая зовёт следующую десять раз,
	// последняя пишет текст.
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Fm 5 0 R >> >> /Contents 4 0 R >>",
		stream("", "/Fm Do"),
	}
	for i := 0; i < 8; i++ {
		if i == 7 {
			objs = append(objs, stream("<< /Type /XObject /Subtype /Form /Resources << /Font << /F1 13 0 R >> >> >>",
				"BT /F1 12 Tf 72 720 Td (leaf) Tj ET"))
			continue
		}
		objs = append(objs, stream(fmt.Sprintf("<< /Type /XObject /Subtype /Form /Resources << /XObject << /Fm %d 0 R >> >> >>", 6+i),
			strings.Repeat("/Fm Do ", 10)))
	}
	chain := build(append(objs, helvetica)...)

	bounded(t, 20*time.Second, func() {
		if got := pageText(t, self); got != "loop" {
			t.Errorf("форма, зовущая себя: %q", got)
		}
		if got := pageText(t, chain); !strings.Contains(got, "leaf") {
			t.Errorf("цепочка форм: %.40q", got)
		}
	})
}

// manyObjects — файл из n объектов с телом body (номер подставляется в %d)
// и трейлером в конце.
func manyObjects(n int, body string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%d 0 obj\n", i)
		fmt.Fprintf(&b, body, i)
		b.WriteString("\n")
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n")
	return b.Bytes()
}

// Незакрытый массив, строка или словарь не съедают остаток файла: прежде
// каждый из тысяч таких объектов разбирался до конца файла, и сотня
// килобайт превращалась в гигабайты разобранного. То же — тысячи трейлеров
// с незакрытым словарём и тысячи потоков с неверной длиной без единого
// «endstream» после них: на каждый остаток файла перебирался заново.
func TestUnclosedObjectsStayLocal(t *testing.T) {
	cases := []struct {
		name  string
		data  []byte
		check func(d *Document) string
	}{
		{"массивы", manyObjects(20000, "[ %d 2 3 4 5 6 7 8 9 10"), func(d *Document) string {
			if a, ok := d.object(7).(Array); !ok || len(a) != 10 || a[0] != int64(7) {
				return fmt.Sprintf("объект 7: %#v", d.object(7))
			}
			return ""
		}},
		{"строки", manyObjects(20000, "(незакрытая строка %d"), func(d *Document) string {
			if s, ok := d.object(7).(String); !ok || len(s) > 60 {
				return fmt.Sprintf("объект 7: строка %d байт", len(s))
			}
			return ""
		}},
		{"словари", manyObjects(20000, "<< /Type /Page /N %d /Kids [1 0 R"), func(d *Document) string {
			if dict, ok := d.object(7).(Dict); !ok || dict["N"] != int64(7) {
				return fmt.Sprintf("объект 7: %#v", d.object(7))
			}
			return ""
		}},
		{"трейлеры", append(append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("trailer\n<< /Info [ "), 30000)...),
			"\ntrailer\n<< /Root 1 0 R >>\n1 0 obj\n<< /Type /Catalog >>\nendobj\n"...), func(d *Document) string {
			if d.trailer["Root"] == nil {
				return "трейлер не найден"
			}
			return ""
		}},
		{"потоки без конца", manyObjects(50000, "<< /Length 999999999 /N %d >>\nstream\nданные"), func(d *Document) string {
			if s, ok := d.object(7).(*Stream); !ok || s.Dict["N"] != int64(7) {
				return fmt.Sprintf("объект 7: %#v", d.object(7))
			}
			return ""
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bounded(t, 10*time.Second, func() {
				d, err := Open(c.data)
				if err != nil {
					t.Errorf("открытие: %v", err)
					return
				}
				if msg := c.check(d); msg != "" {
					t.Error(msg)
				}
			})
		})
	}
}

// Объект, за которым следующий заголовок, разбирается как прежде, а поток
// по /Length вправе уходить за чужой заголовок внутри своих данных.
func TestObjectRegionKeepsStreams(t *testing.T) {
	inner := "1 0 obj\n(ложный заголовок внутри данных)\nendobj\n"
	doc := build(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		stream("", "BT /F1 12 Tf 72 720 Td (text) Tj ET\n% "+inner),
		helvetica)
	d, err := Open(doc)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := d.object(4).(*Stream)
	if !ok || !bytes.Contains(s.Raw, []byte("ложный заголовок")) {
		t.Fatalf("поток обрезан по чужому заголовку: %#v", d.object(4))
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
