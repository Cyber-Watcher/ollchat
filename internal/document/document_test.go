package document

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Пробники обоих форматов собираются прямо здесь: тесты не должны зависеть
// от файлов на машине.

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// samplePDF собирает документ с текстом и картинкой 300×200.
func samplePDF() []byte {
	pixels := make([]byte, 300*200)
	for i := range pixels {
		pixels[i] = byte(i)
	}
	stream := func(dict string, data []byte) string {
		if dict == "" {
			dict = "<< >>"
		}
		dict = strings.TrimSuffix(strings.TrimSpace(dict), ">>")
		return fmt.Sprintf("%s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
	}
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> " +
			"/XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>",
		stream("", []byte("BT /F1 12 Tf 50 700 Td (page text) Tj ET\nq 300 0 0 200 50 400 cm /Im0 Do Q")),
		stream("<< /Type /XObject /Subtype /Image /Width 300 /Height 200 "+
			"/ColorSpace /DeviceGray /BitsPerComponent 8 >>", pixels),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	for i, body := range objs {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return []byte(b.String())
}

// sampleEPUB собирает книгу из одной главы с рисунком.
func sampleEPUB(t *testing.T) []byte {
	t.Helper()
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
		0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0, 0x90, 0x77, 0x53, 0xde,
		0, 0, 0, 0x0c, 'I', 'D', 'A', 'T',
		0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00, 0x03, 0x01, 0x01, 0x00,
		0x18, 0xdd, 0x8d, 0xb0,
		0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
	}
	files := []struct {
		name string
		body []byte
	}{
		{"META-INF/container.xml", []byte(`<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
			<rootfiles><rootfile full-path="OEBPS/book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)},
		{"OEBPS/book.opf", []byte(`<package xmlns="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/" version="3.0">
			<metadata><dc:title>Книга</dc:title><dc:creator>Автор</dc:creator></metadata>
			<manifest><item href="ch1.xhtml" id="c1" media-type="application/xhtml+xml"/></manifest>
			<spine><itemref idref="c1"/></spine></package>`)},
		{"OEBPS/ch1.xhtml", []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body>
			<h1>Глава</h1><p>текст главы</p><img src="pic.png" alt="схема"/></body></html>`)},
		{"OEBPS/pic.png", png},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	w.Write([]byte("application/epub+zip"))
	for _, f := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		w.Write(f.body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDetectFile(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want Kind
	}{
		{"doc.pdf", samplePDF(), KindPDF},
		{"book.epub", sampleEPUB(t), KindEPUB},
		{"note.txt", []byte("обычный текст"), KindNone},
		{"empty.bin", nil, KindNone},
	}
	for _, c := range cases {
		if got := DetectFile(writeTemp(t, c.name, c.data)); got != c.want {
			t.Errorf("%s → %q, ожидалось %q", c.name, got, c.want)
		}
	}
}

// TestDetectByContentNotExtension закрепляет, что формат определяется по
// содержимому: расширение бывает каким угодно, а иногда его нет вовсе.
func TestDetectByContentNotExtension(t *testing.T) {
	if got := DetectFile(writeTemp(t, "книга.txt", sampleEPUB(t))); got != KindEPUB {
		t.Fatalf("книга с чужим расширением не распознана: %q", got)
	}
	if got := DetectFile(writeTemp(t, "документ", samplePDF())); got != KindPDF {
		t.Fatalf("документ без расширения не распознан: %q", got)
	}
}

func TestReadBothFormats(t *testing.T) {
	pdfDoc, err := Read(writeTemp(t, "doc.pdf", samplePDF()), 0)
	if err != nil {
		t.Fatalf("PDF: %v", err)
	}
	if pdfDoc.Kind != KindPDF || pdfDoc.Units != 1 || pdfDoc.Unit != "страниц" {
		t.Fatalf("PDF разобран неверно: %+v", pdfDoc)
	}
	if !strings.Contains(pdfDoc.Text, "page text") || pdfDoc.Figures != 1 {
		t.Fatalf("PDF: текст %q, рисунков %d", pdfDoc.Text, pdfDoc.Figures)
	}

	book, err := Read(writeTemp(t, "book.epub", sampleEPUB(t)), 0)
	if err != nil {
		t.Fatalf("EPUB: %v", err)
	}
	if book.Kind != KindEPUB || book.Units != 1 || book.Unit != "разделов" {
		t.Fatalf("книга разобрана неверно: %+v", book)
	}
	if book.Title != "Книга" || book.Author != "Автор" {
		t.Fatalf("сведения о книге: %q / %q", book.Title, book.Author)
	}
	if !strings.Contains(book.Text, "текст главы") || book.Figures != 1 {
		t.Fatalf("книга: текст %q, рисунков %d", book.Text, book.Figures)
	}
}

// TestHeaderMentionsFigures проверяет, что модели прямо говорят: рисунки в
// тексте лишь отмечены, а приложить их можно командой.
func TestHeaderMentionsFigures(t *testing.T) {
	doc, err := Read(writeTemp(t, "doc.pdf", samplePDF()), 0)
	if err != nil {
		t.Fatal(err)
	}
	head := doc.Header(true)
	if !strings.Contains(head, "Документ PDF, страниц: 1") {
		t.Fatalf("нет сведений о документе: %q", head)
	}
	// Подсказка обязана вести к инструменту: команду /addimg модель вызвать
	// не может, и, увидев метку без способа посмотреть, она идёт искать
	// в системе средства распознавания.
	if !strings.Contains(head, "view_image") {
		t.Fatalf("не сказано, чем посмотреть рисунок: %q", head)
	}
	if strings.Contains(head, "/addimg") {
		t.Fatalf("модели предложена команда пользователя, которую она не может вызвать: %q", head)
	}
}

// TestHeaderWithoutViewImageSaysSo — обратный случай, и он важнее.
//
// Когда инструмент выключен, звать к нему нельзя: модель принимает имя
// за программу и просит подтверждение на «bash view_image …». Проверено
// на живом сеансе с конфигом от прежней версии.
func TestHeaderWithoutViewImageSaysSo(t *testing.T) {
	doc, err := Read(writeTemp(t, "doc.pdf", samplePDF()), 0)
	if err != nil {
		t.Fatal(err)
	}
	head := doc.Header(false)
	if strings.Contains(head, "вызовите инструмент view_image") {
		t.Fatalf("модель зовут к выключенному инструменту: %q", head)
	}
	if !strings.Contains(head, "выключен") {
		t.Fatalf("не сказано, что показать картинку нечем: %q", head)
	}
	// Обходной путь через распознавание должен быть закрыт прямым текстом.
	if !strings.Contains(head, "не помогут") {
		t.Fatalf("не закрыт путь к средствам распознавания: %q", head)
	}
}

func TestImagesBothFormats(t *testing.T) {
	imgs, err := Images(writeTemp(t, "doc.pdf", samplePDF()), 0, ImageOptions{})
	if err != nil {
		t.Fatalf("PDF: %v", err)
	}
	if len(imgs) != 1 || imgs[0].Label != "1.1" || imgs[0].Width != 300 {
		t.Fatalf("картинки PDF разобраны неверно: %+v", imgs)
	}

	imgs, err = Images(writeTemp(t, "book.epub", sampleEPUB(t)), 0, ImageOptions{})
	if err != nil {
		t.Fatalf("EPUB: %v", err)
	}
	if len(imgs) != 1 || imgs[0].Label != "1.1" || imgs[0].Format != "png" {
		t.Fatalf("картинки книги разобраны неверно: %+v", imgs)
	}
}

func TestSizeLimit(t *testing.T) {
	path := writeTemp(t, "doc.pdf", samplePDF())
	if _, err := Read(path, 10); err == nil {
		t.Fatal("предел размера не сработал")
	}
	if _, err := Images(path, 10, ImageOptions{}); err == nil {
		t.Fatal("предел размера не сработал для картинок")
	}
}

func TestReadRejectsOtherFiles(t *testing.T) {
	if _, err := Read(writeTemp(t, "note.txt", []byte("текст")), 0); err == nil {
		t.Fatal("обычный файл прочитан как документ")
	}
}

// TestPartsGivesUnitNumbers — куски базы знаний обязаны знать точный номер
// страницы: по нему модель ссылается на источник, и ссылку можно проверить.
func TestPartsGivesUnitNumbers(t *testing.T) {
	doc, parts, err := Parts(writeTemp(t, "doc.pdf", samplePDF()), 0)
	if err != nil {
		t.Fatalf("PDF: %v", err)
	}
	if doc.Kind != KindPDF || doc.Units != 1 {
		t.Fatalf("сведения о документе неверны: %+v", doc)
	}
	if len(parts) != 1 || parts[0].Number != 1 {
		t.Fatalf("страницы разобраны неверно: %+v", parts)
	}
	if !strings.Contains(parts[0].Text, "page text") {
		t.Fatalf("текст страницы потерян: %q", parts[0].Text)
	}
	// Склеенного текста здесь быть не должно: он не нужен и занимает память.
	if doc.Text != "" {
		t.Fatalf("Parts вернул склеенный текст длиной %d", len(doc.Text))
	}

	doc, parts, err = Parts(writeTemp(t, "book.epub", sampleEPUB(t)), 0)
	if err != nil {
		t.Fatalf("EPUB: %v", err)
	}
	if len(parts) != 1 || parts[0].Number != 1 || parts[0].Title == "" {
		t.Fatalf("разделы книги разобраны неверно: %+v", parts)
	}
}

// TestPartsHaveNoPageMarkers закрепляет требование, найденное на живой книге:
// в куски не должны попадать наши собственные заголовки «── страница N ──».
// Иначе слово «страница» становится одним из самых частых термов индекса
// и портит ранжирование, ничего при этом не значая.
func TestPartsHaveNoPageMarkers(t *testing.T) {
	_, parts, err := Parts(writeTemp(t, "doc.pdf", samplePDF()), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		if strings.Contains(p.Text, "── страница") || strings.Contains(p.Text, "── раздел") {
			t.Fatalf("в кусок попал заголовок единицы: %q", p.Text)
		}
	}
}

// TestProbeIsCheap — быстрая проба разбирает лишь несколько единиц: при обходе
// тысяч книг полный разбор скана ради вывода «текста нет» непозволителен.
func TestProbeIsCheap(t *testing.T) {
	doc, err := Probe(writeTemp(t, "doc.pdf", samplePDF()), 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Units != 1 || !strings.Contains(doc.Text, "page text") {
		t.Fatalf("проба вернула не то: %+v", doc)
	}
}

// TestProbeReportsScan — скан должен опознаваться пробой, а не полным разбором.
func TestProbeReportsScan(t *testing.T) {
	scan := []byte("%PDF-1.7\n" +
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>\nendobj\n" +
		"4 0 obj\n<< /Length 30 >>\nstream\nq 612 0 0 792 0 0 cm /Im0 Do Q\nendstream\nendobj\n" +
		"trailer\n<< /Root 1 0 R >>\n%%EOF\n")
	if _, err := Probe(writeTemp(t, "scan.pdf", scan), 0, 5); err == nil {
		t.Fatal("скан не опознан")
	} else if !strings.Contains(err.Error(), "текстового слоя") {
		t.Fatalf("непонятная причина отказа: %v", err)
	}
}
