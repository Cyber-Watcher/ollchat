package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitPathSpec(t *testing.T) {
	cases := []struct{ arg, path, spec string }{
		{"книга.pdf", "книга.pdf", ""},
		{"книга.pdf 44", "книга.pdf", "44"},
		{"книга.pdf 40-60", "книга.pdf", "40-60"},
		{"мои файлы/книга 2024.pdf", "мои файлы/книга 2024.pdf", ""},
		{"мои файлы/книга 2024.pdf 12", "мои файлы/книга 2024.pdf", "12"},
		{"", "", ""},
	}
	for _, c := range cases {
		path, spec := splitPathSpec(c.arg)
		if path != c.path || spec != c.spec {
			t.Errorf("%q → путь %q, страницы %q; ожидалось %q и %q", c.arg, path, spec, c.path, c.spec)
		}
	}
}

func TestParsePageSpec(t *testing.T) {
	cases := []struct {
		spec         string
		first, count int
		bad          bool
	}{
		{"", 1, 0, false},
		{"44", 44, 1, false},
		{"40-60", 40, 21, false},
		{"0", 0, 0, true},
		{"60-40", 0, 0, true},
		{"abc", 0, 0, true},
	}
	for _, c := range cases {
		first, count, err := parsePageSpec(c.spec)
		if c.bad {
			if err == nil {
				t.Errorf("%q: ожидалась ошибка", c.spec)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.spec, err)
			continue
		}
		if first != c.first || count != c.count {
			t.Errorf("%q → %d/%d, ожидалось %d/%d", c.spec, first, count, c.first, c.count)
		}
	}
}

// samplePDFWithImage собирает документ с одной картинкой 300×200 на странице.
func samplePDFWithImage() []byte {
	pixels := make([]byte, 300*200*3)
	for i := range pixels {
		pixels[i] = byte(i % 251)
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
		stream("", []byte("BT /F1 12 Tf 50 700 Td (схема ниже) Tj ET\nq 300 0 0 200 50 400 cm /Im0 Do Q")),
		stream("<< /Type /XObject /Subtype /Image /Width 300 /Height 200 "+
			"/ColorSpace /DeviceRGB /BitsPerComponent 8 >>", pixels),
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

// TestAddImagesCommand проверяет команду целиком: рисунок должен стать
// вложением, метка — попасть в промпт, а в ленте должно появиться сообщение
// о том, какой [ImageNN] какому рисунку соответствует.
func TestAddImagesCommand(t *testing.T) {
	m := newTestModel(t)
	path := filepath.Join(m.guard.Sandbox().RealRoot(), "книга.pdf")
	if err := os.WriteFile(path, samplePDFWithImage(), 0o644); err != nil {
		t.Fatal(err)
	}

	m.runCommand("/addimg книга.pdf")

	if len(m.pending) != 1 {
		t.Fatalf("вложений %d, ожидалось одно", len(m.pending))
	}
	got := m.pending[0]
	if got.w != 300 || got.h != 200 {
		t.Fatalf("размер вложения %d×%d, ожидалось 300×200", got.w, got.h)
	}
	if got.mime != "image/png" {
		t.Fatalf("тип вложения %q", got.mime)
	}
	if !strings.Contains(m.ta.Value(), "[Image01]") {
		t.Fatalf("метка не вставлена в промпт: %q", m.ta.Value())
	}
	last := m.blocks[len(m.blocks)-1]
	if !strings.Contains(last.text, "[Image01] = рисунок 1.1") {
		t.Fatalf("в ленте нет связи метки с рисунком: %q", last.text)
	}
}

// TestAddImagesRejectsNonPDF — команда работает только с документами PDF.
func TestAddImagesRejectsNonPDF(t *testing.T) {
	m := newTestModel(t)
	path := filepath.Join(m.guard.Sandbox().RealRoot(), "заметка.txt")
	if err := os.WriteFile(path, []byte("обычный текст"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.runCommand("/addimg заметка.txt")
	if len(m.pending) != 0 {
		t.Fatal("к вопросу приложено вложение из обычного файла")
	}
	if last := m.blocks[len(m.blocks)-1]; last.kind != blockError {
		t.Fatalf("ожидалась ошибка, получено: %q", last.text)
	}
}

// TestAddFilePDFMentionsFigures проверяет, что /add сообщает о рисунках и о
// том, чем их приложить: иначе модель начнёт рассуждать о том, чего не видела.
func TestAddFilePDFMentionsFigures(t *testing.T) {
	m := newTestModel(t)
	path := filepath.Join(m.guard.Sandbox().RealRoot(), "книга.pdf")
	if err := os.WriteFile(path, samplePDFWithImage(), 0o644); err != nil {
		t.Fatal(err)
	}
	m.runCommand("/add книга.pdf")

	msgs := m.conv.Request()
	body := msgs[len(msgs)-1].Content
	if !strings.Contains(body, "[рисунок 1.1: 300×200]") {
		t.Fatalf("метки рисунка нет в приложенном тексте:\n%s", body)
	}
	if !strings.Contains(body, "view_image") {
		t.Fatalf("не сказано, чем посмотреть рисунки:\n%s", body)
	}
}
