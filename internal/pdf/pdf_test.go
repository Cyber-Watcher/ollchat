package pdf

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// build собирает документ из готовых тел объектов: первый объект получает
// номер 1. Таблица xref не пишется намеренно — разбор на неё не опирается,
// и тесты заодно это закрепляют.
func build(objs ...string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	for i, body := range objs {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	b.WriteString("trailer\n<< /Root 1 0 R /Size 99 >>\n%%EOF\n")
	return b.Bytes()
}

// stream оформляет поток с правильной длиной.
func stream(dict, data string) string {
	if dict == "" {
		dict = "<< >>"
	}
	dict = strings.TrimSuffix(strings.TrimSpace(dict), ">>")
	return fmt.Sprintf("%s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

// docWith собирает документ из одной страницы с заданными шрифтами и содержимым.
func docWith(fontRes, content string, extra ...string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << " +
			fontRes + " >> >> /Contents 4 0 R >>",
		stream("", content),
	}
	return build(append(objs, extra...)...)
}

func extract(t *testing.T, data []byte) string {
	t.Helper()
	res, err := Extract(data, Options{})
	if err != nil {
		t.Fatalf("извлечение: %v", err)
	}
	return res.Pages[0].Text
}

func TestExtractSimpleText(t *testing.T) {
	doc := docWith(
		"/F1 5 0 R",
		"BT /F1 12 Tf 72 720 Td (Hello World) Tj ET",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	)
	if got := extract(t, doc); got != "Hello World" {
		t.Fatalf("получено %q, ожидалось %q", got, "Hello World")
	}
}

// TestExtractIdentityHCyrillic проверяет главный для нас случай: составной шрифт
// с двухбайтовыми кодами и таблицей /ToUnicode — так устроен русский текст
// почти во всех современных документах.
func TestExtractIdentityHCyrillic(t *testing.T) {
	cmap := `/CIDInit /ProcSet findresource begin
begincmap
1 begincodespacerange
<0000> <FFFF>
endcodespacerange
3 beginbfchar
<0003> <0414>
<0004> <0430>
<0005> <0020>
endbfchar
endcmap`
	doc := docWith(
		"/F1 5 0 R",
		"BT /F1 12 Tf 72 720 Td <000300040005000300040005> Tj ET",
		"<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H "+
			"/DescendantFonts [7 0 R] /ToUnicode 6 0 R >>",
		stream("", cmap),
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X /DW 500 >>",
	)
	if got := extract(t, doc); got != "Да Да" {
		t.Fatalf("получено %q, ожидалось %q", got, "Да Да")
	}
}

// TestSimpleFontIgnoresCodespaceWidth закрепляет исправленный дефект: таблица
// /ToUnicode однобайтового шрифта сплошь и рядом объявляет диапазон
// <0000>–<FFFF>. Если поверить ей и читать по два байта, текст превращается
// в мусор — ширину кода задаёт только тип шрифта.
func TestSimpleFontIgnoresCodespaceWidth(t *testing.T) {
	cmap := `begincmap
1 begincodespacerange
<0000> <FFFF>
endcodespacerange
2 beginbfchar
<41> <0410>
<42> <0411>
endbfchar
endcmap`
	doc := docWith(
		"/F1 5 0 R",
		"BT /F1 12 Tf 72 720 Td (ABAB) Tj ET",
		"<< /Type /Font /Subtype /TrueType /BaseFont /X /Encoding /WinAnsiEncoding /ToUnicode 6 0 R >>",
		stream("", cmap),
	)
	if got := extract(t, doc); got != "АБАБ" {
		t.Fatalf("получено %q, ожидалось %q", got, "АБАБ")
	}
}

// TestBigRangeStoredAsRange проверяет, что сплошной диапазон не разворачивается
// поэлементно, но по-прежнему находится. Развёрнутые таблицы на 65536 записей
// у тысяч шрифтов съедали полтора гигабайта памяти.
func TestBigRangeStoredAsRange(t *testing.T) {
	cmap := `begincmap
1 beginbfrange
<0100> <01FF> <0410>
endbfrange
endcmap`
	doc := docWith(
		"/F1 5 0 R",
		"BT /F1 12 Tf 72 720 Td <010001010102> Tj ET",
		"<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /ToUnicode 6 0 R >>",
		stream("", cmap),
	)
	res, err := Extract(doc, Options{})
	if err != nil {
		t.Fatalf("извлечение: %v", err)
	}
	if got := res.Pages[0].Text; got != "АБВ" {
		t.Fatalf("получено %q, ожидалось %q", got, "АБВ")
	}
}

// TestMeaninglessGlyphNames закрепляет честный отказ: если /Differences задаёт
// имена вроде «a22», подставлять вместо них латиницу нельзя — получится связная
// с виду бессмыслица, и модель начнёт её толковать.
func TestMeaninglessGlyphNames(t *testing.T) {
	doc := docWith(
		"/F1 5 0 R",
		"BT /F1 12 Tf 72 720 Td (\\301\\302\\303) Tj ET",
		"<< /Type /Font /Subtype /Type3 /FontMatrix [0.001 0 0 0.001 0 0] "+
			"/Encoding << /Differences [193 /a1 /a22 /a33] >> /CharProcs << >> >>",
	)
	_, err := Extract(doc, Options{})
	if !errors.Is(err, ErrGarbledText) {
		t.Fatalf("ожидался ErrGarbledText, получено %v", err)
	}
}

func TestScannedDocumentReported(t *testing.T) {
	doc := docWith("", "q 612 0 0 792 0 0 cm /Im0 Do Q")
	_, err := Extract(doc, Options{})
	if !errors.Is(err, ErrNoText) {
		t.Fatalf("ожидался ErrNoText, получено %v", err)
	}
}

func TestEncryptedDocumentReported(t *testing.T) {
	doc := bytes.Replace(
		docWith("/F1 5 0 R", "BT (x) Tj ET", "<< /Type /Font /Subtype /Type1 >>"),
		[]byte("/Root 1 0 R"), []byte("/Root 1 0 R /Encrypt 9 0 R"), 1)
	_, err := Extract(doc, Options{})
	if !errors.Is(err, ErrEncrypted) {
		t.Fatalf("ожидался ErrEncrypted, получено %v", err)
	}
}

func TestNotPDF(t *testing.T) {
	if _, err := Extract([]byte("обычный текст"), Options{}); !errors.Is(err, ErrNotPDF) {
		t.Fatalf("ожидался ErrNotPDF, получено %v", err)
	}
}

// TestGlyphWidthsPreventLetterSpacing закрепляет исправленный дефект: без учёта
// ширины глифов каждый сдвиг пера выглядел прыжком вправо, и текст выходил
// рассыпанным по буквам — «П Р И В Е Т» вместо «ПРИВЕТ».
func TestGlyphWidthsPreventLetterSpacing(t *testing.T) {
	// Шрифт шириной 500/1000 em: при кегле 10 каждая буква занимает 5 единиц.
	font := "<< /Type /Font /Subtype /Type1 /BaseFont /X /Encoding /WinAnsiEncoding " +
		"/FirstChar 65 /LastChar 67 /Widths [500 500 500] >>"
	// Буквы поставлены вплотную, затем — с настоящим разрывом.
	content := "BT /F1 10 Tf 0 700 Td (A) Tj 5 0 Td (B) Tj 40 0 Td (C) Tj ET"
	got := extract(t, docWith("/F1 5 0 R", content, font))
	// Разрыв сохраняется отступом, поэтому проверяется состав, а не длина:
	// «A» и «B» обязаны склеиться, «C» — отделиться.
	fields := strings.Fields(got)
	if len(fields) != 2 || fields[0] != "AB" || fields[1] != "C" {
		t.Fatalf("получено %q, ожидалось два слова «AB» и «C»", got)
	}
}

func TestPagesInOrderAndCounted(t *testing.T) {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /Resources << /Font << /F1 7 0 R >> >> >>",
		"<< /Type /Page /Parent 2 0 R /Contents 5 0 R >>",
		"<< /Type /Page /Parent 2 0 R /Contents 6 0 R >>",
		stream("", "BT /F1 12 Tf 10 700 Td (page one) Tj ET"),
		stream("", "BT /F1 12 Tf 10 700 Td (page two) Tj ET"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	res, err := Extract(build(objs...), Options{})
	if err != nil {
		t.Fatalf("извлечение: %v", err)
	}
	if res.TotalPages != 2 {
		t.Fatalf("страниц %d, ожидалось 2", res.TotalPages)
	}
	if !strings.Contains(res.Pages[0].Text, "page one") || !strings.Contains(res.Pages[1].Text, "page two") {
		t.Fatalf("порядок страниц нарушен: %q и %q", res.Pages[0].Text, res.Pages[1].Text)
	}
	// Ресурсы наследуются от узла дерева — без этого шрифт бы не нашёлся.
	if res.Pages[0].Text == "" {
		t.Fatal("наследование /Resources не работает")
	}
}

func TestPageWindow(t *testing.T) {
	var objs []string
	objs = append(objs,
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 /Resources << /Font << /F1 9 0 R >> >> >>",
		"<< /Type /Page /Parent 2 0 R /Contents 6 0 R >>",
		"<< /Type /Page /Parent 2 0 R /Contents 7 0 R >>",
		"<< /Type /Page /Parent 2 0 R /Contents 8 0 R >>",
		stream("", "BT /F1 12 Tf 10 700 Td (one) Tj ET"),
		stream("", "BT /F1 12 Tf 10 700 Td (two) Tj ET"),
		stream("", "BT /F1 12 Tf 10 700 Td (three) Tj ET"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	res, err := Extract(build(objs...), Options{FirstPage: 2, MaxPages: 1})
	if err != nil {
		t.Fatalf("извлечение: %v", err)
	}
	if len(res.Pages) != 1 || res.Pages[0].Number != 2 || res.Pages[0].Text != "two" {
		t.Fatalf("окно страниц не соблюдено: %+v", res.Pages)
	}
	if !res.Truncated {
		t.Fatal("признак усечения не выставлен")
	}
}

// TestObjectStream проверяет разбор сжатых объектов: в файлах версии 1.5 и новее
// каталог и страницы лежат именно там.
func TestObjectStream(t *testing.T) {
	inner := "<< /Type /Catalog /Pages 2 0 R >> << /Type /Pages /Kids [3 0 R] /Count 1 >>"
	head := "1 0 2 34 "
	body := head + inner
	var packed bytes.Buffer
	zw := zlib.NewWriter(&packed)
	zw.Write([]byte(body))
	zw.Close()

	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	fmt.Fprintf(&b, "4 0 obj\n<< /Type /ObjStm /N 2 /First %d /Filter /FlateDecode /Length %d >>\nstream\n",
		len(head), packed.Len())
	b.Write(packed.Bytes())
	b.WriteString("\nendstream\nendobj\n")
	fmt.Fprintf(&b, "3 0 obj\n<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 6 0 R >>\nendobj\n")
	fmt.Fprintf(&b, "5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")
	fmt.Fprintf(&b, "6 0 obj\n%s\nendobj\n", stream("", "BT /F1 12 Tf 10 700 Td (compressed) Tj ET"))
	b.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")

	res, err := Extract(b.Bytes(), Options{})
	if err != nil {
		t.Fatalf("извлечение: %v", err)
	}
	if res.Pages[0].Text != "compressed" {
		t.Fatalf("получено %q", res.Pages[0].Text)
	}
}

func TestFormXObjectText(t *testing.T) {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Fm0 5 0 R >> >> /Contents 4 0 R >>",
		stream("", "q /Fm0 Do Q"),
		stream("<< /Type /XObject /Subtype /Form /Resources << /Font << /F1 6 0 R >> >> >>",
			"BT /F1 12 Tf 10 700 Td (inside form) Tj ET"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	if got := extract(t, build(objs...)); got != "inside form" {
		t.Fatalf("получено %q", got)
	}
}

func TestInlineImageSkipped(t *testing.T) {
	content := "BT /F1 12 Tf 10 700 Td (before) Tj ET\n" +
		"BI /W 2 /H 2 /BPC 8 /CS /G ID \x00\xff(\\Tj garbage\xfe EI\n" +
		"BT /F1 12 Tf 10 680 Td (after) Tj ET"
	doc := docWith("/F1 5 0 R", content, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	got := extract(t, doc)
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Fatalf("текст вокруг встроенного изображения потерян: %q", got)
	}
	if strings.Contains(got, "garbage") {
		t.Fatalf("двоичные данные изображения попали в текст: %q", got)
	}
}

func TestStringEscapes(t *testing.T) {
	p := newParser([]byte(`(a\(b\)c\\d\101\n)`), nil)
	obj, err := p.object()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(obj.(String)); got != "a(b)c\\dA\n" {
		t.Fatalf("получено %q", got)
	}
}

func TestParserRefsAndNumbers(t *testing.T) {
	p := newParser([]byte("<< /A 12 0 R /B 3.5 /C -7 /D /Имя#20с /E [1 2] >>"), nil)
	obj, err := p.object()
	if err != nil {
		t.Fatal(err)
	}
	d, ok := obj.(Dict)
	if !ok {
		t.Fatalf("ожидался словарь, получено %T", obj)
	}
	if d["A"] != (Ref{Num: 12, Gen: 0}) {
		t.Fatalf("ссылка разобрана неверно: %#v", d["A"])
	}
	if d["B"] != 3.5 || d["C"] != int64(-7) {
		t.Fatalf("числа разобраны неверно: %#v %#v", d["B"], d["C"])
	}
	if d["D"] != Name("Имя с") {
		t.Fatalf("имя с экранированием разобрано неверно: %#v", d["D"])
	}
}

func TestFilters(t *testing.T) {
	if got := string(asciiHexDecode([]byte("48656C6C6F>"))); got != "Hello" {
		t.Fatalf("ASCIIHex: %q", got)
	}
	if got := string(ascii85Decode([]byte("87cURD_*#TDfTZ)~>"))); got != "Hello, world" {
		t.Fatalf("ASCII85: %q", got)
	}
	if got := string(runLengthDecode([]byte{2, 'a', 'b', 'c', 254, 'z', 128})); got != "abczzz" {
		t.Fatalf("RunLength: %q", got)
	}
}

func TestPredictorPNGUp(t *testing.T) {
	d := &Document{}
	// Две строки по три байта, фильтр Up: вторая строка задана приращением.
	data := []byte{2, 1, 2, 3, 2, 1, 1, 1}
	out, err := d.predict(data, Dict{"Predictor": int64(12), "Columns": int64(3)})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 2, 3, 2, 3, 4}
	if !bytes.Equal(out, want) {
		t.Fatalf("получено %v, ожидалось %v", out, want)
	}
}

func TestTextStringDecoding(t *testing.T) {
	utf16 := append([]byte{0xFE, 0xFF}, 0x04, 0x14, 0x04, 0x30)
	if got := decodeTextString(utf16); got != "Да" {
		t.Fatalf("UTF-16: %q", got)
	}
	if got := decodeTextString([]byte("Plain")); got != "Plain" {
		t.Fatalf("однобайтовая: %q", got)
	}
}

// TestBrokenLengthRecovered проверяет устойчивость к битому /Length: длина
// потока в живых файлах бывает неверной, и разбор обязан это пережить.
func TestBrokenLengthRecovered(t *testing.T) {
	content := "BT /F1 12 Tf 10 700 Td (plain text) Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length 99999 >>\nstream\n%s\nendstream", content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	if got := extract(t, build(objs...)); got != "plain text" {
		t.Fatalf("получено %q", got)
	}
}

// TestExtractImagesPNG проверяет извлечение картинки, лежащей несжатыми
// отсчётами: такие переупаковываются в PNG, потому что модели нужен обычный
// формат, а не сырой растр PDF.
func TestExtractImagesPNG(t *testing.T) {
	// Картинка 2×2, цвета: красный, зелёный, синий, белый.
	pixels := string([]byte{
		255, 0, 0, 0, 255, 0,
		0, 0, 255, 255, 255, 255,
	})
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>",
		stream("", "q 200 0 0 200 50 600 cm /Im0 Do Q"),
		stream("<< /Type /XObject /Subtype /Image /Width 2 /Height 2 "+
			"/ColorSpace /DeviceRGB /BitsPerComponent 8 >>", pixels),
	}
	imgs, err := ExtractImages(build(objs...), ImageOptions{})
	if err != nil {
		t.Fatalf("извлечение: %v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("картинок %d, ожидалась одна", len(imgs))
	}
	im := imgs[0]
	if im.Format != "png" || im.Width != 2 || im.Height != 2 {
		t.Fatalf("получено %s %d×%d", im.Format, im.Width, im.Height)
	}
	if im.Label() != "1.1" {
		t.Fatalf("обозначение %q, ожидалось «1.1»", im.Label())
	}
	if len(im.Data) < 8 || string(im.Data[1:4]) != "PNG" {
		t.Fatal("данные не похожи на PNG")
	}
}

// TestImageMarkerInText закрепляет связь текста и картинок: метка обязана
// стоять там, где рисунок нарисован, и её номер должен совпадать с тем, что
// вернёт ExtractImages — иначе «покажи рисунок 2» покажет не то.
func TestImageMarkerInText(t *testing.T) {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> " +
			"/XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>",
		stream("", "BT /F1 12 Tf 50 700 Td (before) Tj ET\n"+
			"q 200 0 0 100 50 560 cm /Im0 Do Q\n"+
			"BT /F1 12 Tf 50 500 Td (after) Tj ET"),
		stream("<< /Type /XObject /Subtype /Image /Width 4 /Height 2 "+
			"/ColorSpace /DeviceGray /BitsPerComponent 8 >>", "01234567"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	doc := build(objs...)
	text := extract(t, doc)
	if !strings.Contains(text, "[рисунок 1.1: 4×2]") {
		t.Fatalf("метки рисунка нет в тексте:\n%s", text)
	}
	iBefore := strings.Index(text, "before")
	iMark := strings.Index(text, "[рисунок")
	iAfter := strings.Index(text, "after")
	if !(iBefore < iMark && iMark < iAfter) {
		t.Fatalf("метка стоит не на своём месте:\n%s", text)
	}
	imgs, err := ExtractImages(doc, ImageOptions{})
	if err != nil || len(imgs) != 1 {
		t.Fatalf("картинки: %v, %d штук", err, len(imgs))
	}
	if imgs[0].Index != 1 {
		t.Fatalf("нумерация картинок разошлась с метками: %d", imgs[0].Index)
	}
}

// TestExtractImagesSkipsSmall проверяет отсев мелочи: линейки и значки вёрстки
// вложениями быть не должны.
func TestExtractImagesSkipsSmall(t *testing.T) {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>",
		stream("", "q 100 0 0 100 0 0 cm /Im0 Do Q"),
		stream("<< /Type /XObject /Subtype /Image /Width 2 /Height 2 "+
			"/ColorSpace /DeviceGray /BitsPerComponent 8 >>", "abcd"),
	}
	imgs, err := ExtractImages(build(objs...), ImageOptions{MinWidth: 150, MinHeight: 150})
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 0 {
		t.Fatalf("мелкая картинка не отсеяна: %d", len(imgs))
	}
}

// TestDropIndexAnchors — метки предметного указателя не должны попадать в текст.
//
// Строка взята с 26-й страницы настоящей книги из библиотеки владельца:
// издательский конвейер ставит невидимые якоря, и они склеиваются со словами.
func TestDropIndexAnchors(t *testing.T) {
	got := dropIndexAnchors("tools.Bash oridx_07e7f06fidx_1e43d732 Python scripts")
	if want := "tools.Bash or Python scripts"; got != want {
		t.Fatalf("метки не убраны: %q", got)
	}
	// Похожее, но не якорь: длина не та, регистр не тот, разделитель другой.
	for _, s := range []string{"idx_07e7f06", "idx_07E7F06F", "idx-07e7f06f", "index_07e7f06f "} {
		if dropIndexAnchors(s) != s {
			t.Fatalf("выброшено лишнее: %q → %q", s, dropIndexAnchors(s))
		}
	}
	// Текста без меток правило не касается вовсе.
	const plain = "обычный текст без меток"
	if dropIndexAnchors(plain) != plain {
		t.Fatal("правило тронуло обычный текст")
	}
}
