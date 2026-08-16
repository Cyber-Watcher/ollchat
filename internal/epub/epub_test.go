package epub

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

// buildEPUB собирает книгу в памяти. store говорит, класть ли mimetype без
// сжатия, как велит спецификация: часть книг его всё-таки сжимает, и это
// отдельный случай для проверки.
func buildEPUB(t *testing.T, store bool, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	method := zip.Store
	if !store {
		method = zip.Deflate
	}
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: method})
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("application/epub+zip"))

	for name, body := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const container = `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`

// opf собирает файл пакета. В метаданных намеренно стоит <meta …>aut</meta>
// с содержимым: именно на нём спотыкался разбор, когда к служебным файлам
// применялись правила автозакрытия HTML.
const opf = `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/" version="3.0" unique-identifier="bookid">
  <metadata>
    <dc:title>Книга о разном</dc:title>
    <dc:creator id="creator0">И. И. Иванов</dc:creator>
    <meta refines="#creator0" property="role">aut</meta>
    <dc:language>ru</dc:language>
  </metadata>
  <manifest>
    <item href="ch2.xhtml" id="two" media-type="application/xhtml+xml"/>
    <item href="ch1.xhtml" id="one" media-type="application/xhtml+xml"/>
    <item href="cover.xhtml" id="cover" media-type="application/xhtml+xml"/>
    <item href="toc.ncx" id="ncx" media-type="application/x-dtbncx+xml"/>
  </manifest>
  <spine toc="ncx">
    <itemref idref="cover" linear="no"/>
    <itemref idref="one"/>
    <itemref idref="two"/>
  </spine>
</package>`

const chapter1 = `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>Глава первая</title><style>p { color: red }</style></head>
<body>
  <h1>Глава первая</h1>
  <p>Первый абзац с&nbsp;неразрывным пробелом.</p>
  <p>Второй абзац, где <em>выделено</em> слово.</p>
  <ul><li>первый пункт</li><li>второй пункт</li></ul>
  <table><tr><th>Ключ</th><th>Значение</th></tr><tr><td>a</td><td>1</td></tr></table>
  <script>var x = 1;</script>
</body></html>`

const chapter2 = `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><body>
  <p>Текст до рисунка.</p>
  <img src="images/pic.png" alt="схема устройства"/>
  <p>Текст после рисунка.</p>
</body></html>`

const ncx = `<?xml version="1.0"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
<navMap>
  <navPoint id="n1"><navLabel><text>Первая</text></navLabel><content src="ch1.xhtml"/></navPoint>
  <navPoint id="n2"><navLabel><text>Вторая</text></navLabel><content src="ch2.xhtml"/></navPoint>
</navMap></ncx>`

// pngPixel — картинка 1×1, чтобы книга ссылалась на настоящий файл.
var pngPixel = string([]byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
	0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0, 0x90, 0x77, 0x53, 0xde,
	0, 0, 0, 0x0c, 'I', 'D', 'A', 'T',
	0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00, 0x03, 0x01, 0x01, 0x00,
	0x18, 0xdd, 0x8d, 0xb0,
	0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
})

func sampleBook(t *testing.T, store bool) []byte {
	t.Helper()
	return buildEPUB(t, store, map[string]string{
		"META-INF/container.xml": container,
		"OEBPS/content.opf":      opf,
		"OEBPS/ch1.xhtml":        chapter1,
		"OEBPS/ch2.xhtml":        chapter2,
		"OEBPS/cover.xhtml":      `<html><body><p>обложка</p></body></html>`,
		"OEBPS/toc.ncx":          ncx,
		"OEBPS/images/pic.png":   pngPixel,
	})
}

func TestExtractBook(t *testing.T) {
	res, err := Extract(sampleBook(t, true), Options{})
	if err != nil {
		t.Fatalf("извлечение: %v", err)
	}
	if res.Title != "Книга о разном" || res.Author != "И. И. Иванов" {
		t.Fatalf("сведения о книге: %q / %q", res.Title, res.Author)
	}
	// Обложка помечена linear="no" и в чтение не идёт, поэтому разделов два.
	if res.TotalSections != 2 {
		t.Fatalf("разделов %d, ожидалось 2", res.TotalSections)
	}
	if res.Sections[0].Title != "Глава первая" {
		t.Fatalf("заголовок раздела: %q", res.Sections[0].Title)
	}
	text := res.Sections[0].Text
	for _, want := range []string{
		"Глава первая",
		"Первый абзац с неразрывным пробелом.",
		"Второй абзац, где выделено слово.",
		"• первый пункт",
		"Ключ | Значение",
		"a | 1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в тексте нет %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "var x") || strings.Contains(text, "color: red") {
		t.Errorf("в текст попало содержимое script или style:\n%s", text)
	}
}

// TestSpineOrder закрепляет главное про EPUB: порядок чтения задаёт spine,
// а не имена файлов и не порядок в манифесте.
func TestSpineOrder(t *testing.T) {
	res, err := Extract(sampleBook(t, true), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Sections[0].Text, "Первый абзац") {
		t.Fatalf("первым разделом идёт не ch1:\n%s", res.Sections[0].Text)
	}
	if !strings.Contains(res.Sections[1].Text, "Текст до рисунка") {
		t.Fatalf("вторым разделом идёт не ch2:\n%s", res.Sections[1].Text)
	}
}

// TestOPFMetaWithContent закрепляет исправленный дефект: к служебным файлам
// нельзя применять правила автозакрытия HTML. В списке автозакрываемых есть
// meta, а в OPF стоит <meta …>aut</meta> с содержимым — разбор обрывался,
// и книга выглядела пустой.
func TestOPFMetaWithContent(t *testing.T) {
	if !strings.Contains(opf, "<meta refines=\"#creator0\" property=\"role\">aut</meta>") {
		t.Fatal("тест потерял смысл: в образце OPF больше нет meta с содержимым")
	}
	res, err := Extract(sampleBook(t, true), Options{})
	if err != nil {
		t.Fatalf("книга с <meta>…</meta> в OPF не прочиталась: %v", err)
	}
	if res.TotalSections == 0 {
		t.Fatal("список глав пуст")
	}
}

// TestCompressedMimetype закрепляет второй исправленный дефект: спецификация
// требует хранить mimetype несжатым, но так делают не все, и признак формата
// приходится искать по имени обязательного container.xml.
func TestCompressedMimetype(t *testing.T) {
	data := sampleBook(t, false)
	if bytes.Contains(data[:256], []byte("application/epub+zip")) {
		t.Fatal("тест потерял смысл: mimetype оказался несжатым")
	}
	if !IsEPUB(data) {
		t.Fatal("книга со сжатым mimetype не распознана")
	}
	if _, err := Extract(data, Options{}); err != nil {
		t.Fatalf("извлечение: %v", err)
	}
}

func TestSectionWindow(t *testing.T) {
	data := sampleBook(t, true)

	// Со второго раздела: он последний, значит усекать нечего.
	res, err := Extract(data, Options{FirstSection: 2, MaxSections: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sections) != 1 || res.Sections[0].Number != 2 {
		t.Fatalf("окно разделов не соблюдено: %+v", res.Sections)
	}
	if res.Truncated {
		t.Fatal("последний раздел ошибочно помечен усечением")
	}

	// С первого и только один — второй остался за кадром.
	res, err = Extract(data, Options{MaxSections: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sections) != 1 || !res.Truncated {
		t.Fatalf("усечение не отмечено: разделов %d, признак %v", len(res.Sections), res.Truncated)
	}
}

func TestImageMarkerAndExtraction(t *testing.T) {
	data := sampleBook(t, true)
	res, err := Extract(data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Sections[1].Text
	if !strings.Contains(text, "[рисунок 2.1: схема устройства]") {
		t.Fatalf("метки рисунка нет или она без описания:\n%s", text)
	}
	if strings.Index(text, "Текст до рисунка") > strings.Index(text, "[рисунок") {
		t.Fatalf("метка стоит не на своём месте:\n%s", text)
	}

	imgs, err := ExtractImages(data, ImageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 {
		t.Fatalf("картинок %d, ожидалась одна", len(imgs))
	}
	if imgs[0].Label() != "2.1" {
		t.Fatalf("нумерация картинок разошлась с метками: %q", imgs[0].Label())
	}
	if imgs[0].Format != "png" || imgs[0].Name != "pic.png" {
		t.Fatalf("картинка распознана неверно: %s %s", imgs[0].Format, imgs[0].Name)
	}
}

func TestImagesSkipSmall(t *testing.T) {
	imgs, err := ExtractImages(sampleBook(t, true), ImageOptions{MinWidth: 150, MinHeight: 150})
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 0 {
		t.Fatalf("мелкая картинка не отсеяна: %d", len(imgs))
	}
}

func TestNotEPUB(t *testing.T) {
	if _, err := Extract([]byte("обычный текст"), Options{}); !errors.Is(err, ErrNotEPUB) {
		t.Fatalf("ожидался ErrNotEPUB, получено %v", err)
	}
}
