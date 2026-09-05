package kb

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/document"
)

// lineParts делает части так же, как их отдаёт document.Parts для текста:
// одна строка — одна часть, заголовок markdown тянется вниз по тексту.
func lineParts(text string) []document.Part {
	var out []document.Part
	head := ""
	for i, l := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(l); strings.HasPrefix(s, "# ") || strings.HasPrefix(s, "## ") {
			head = strings.TrimSpace(strings.TrimLeft(s, "#"))
		}
		out = append(out, document.Part{Number: i + 1, Title: head, Text: l})
	}
	return out
}

// Ссылка на кусок текста идёт по строкам, и номера настоящие.
func TestSplitTextRefersToLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "строка %d про настройку таймаутов и окно контекста\n", i)
	}
	chunks := SplitText(lineParts(b.String()), DefaultChunkOpts())
	if len(chunks) < 3 {
		t.Fatalf("кусков вышло %d, ожидалось хотя бы три", len(chunks))
	}
	for i, c := range chunks {
		if c.UnitFrom < 1 || c.UnitTo < c.UnitFrom || c.UnitTo > 301 {
			t.Fatalf("кусок %d ссылается на строки %d–%d — так не бывает", i, c.UnitFrom, c.UnitTo)
		}
		want := fmt.Sprintf("строка %d ", c.UnitFrom)
		if !strings.Contains(c.Text, want) {
			t.Errorf("кусок %d начинается со строки %d, а текста %q в нём нет", i, c.UnitFrom, want)
		}
	}
	if chunks[0].UnitFrom != 1 {
		t.Errorf("первый кусок начинается со строки %d", chunks[0].UnitFrom)
	}
}

// Куски идут с перекрытием: мысль на границе не теряется.
func TestSplitTextOverlaps(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "строка %d содержит достаточно текста, чтобы куски набирались\n", i)
	}
	chunks := SplitText(lineParts(b.String()), DefaultChunkOpts())
	if len(chunks) < 2 {
		t.Fatal("для проверки перекрытия нужно хотя бы два куска")
	}
	if chunks[1].UnitFrom > chunks[0].UnitTo {
		t.Errorf("перекрытия нет: первый кусок кончается на %d, второй начинается с %d",
			chunks[0].UnitTo, chunks[1].UnitFrom)
	}
}

// Заголовок раздела дописывается к куску, который начинается не с него.
func TestSplitTextCarriesHeading(t *testing.T) {
	text := "# Руководство\n\n## Настройка таймаутов\n\n" +
		strings.Repeat("Подробное описание поведения при обрыве связи и повторах.\n", 40)
	chunks := SplitText(lineParts(text), DefaultChunkOpts())
	if len(chunks) < 2 {
		t.Fatalf("кусков %d, нужно хотя бы два", len(chunks))
	}
	// Первый начинается с самого заголовка — приписывать нечего.
	if strings.HasPrefix(chunks[0].Text, "‹") {
		t.Errorf("кусок, начинающийся с заголовка, не должен получать приписку: %q",
			firstLine(chunks[0].Text))
	}
	// Следующий — из середины раздела, и заголовок ему нужен.
	last := chunks[len(chunks)-1]
	if !strings.HasPrefix(last.Text, "‹ Настройка таймаутов ›") {
		t.Errorf("к куску из середины раздела не приписан заголовок: %q", firstLine(last.Text))
	}
}

// Ограждённый блок кода помечается флагом даже тогда, когда кусок начался
// внутри него: иначе модель примет вывод команд за прозу.
func TestSplitTextMarksCode(t *testing.T) {
	text := "# Сборка\n\n```\n" + strings.Repeat("go test ./... && go vet ./...\n", 60) + "```\n"
	chunks := SplitText(lineParts(text), DefaultChunkOpts())
	if len(chunks) < 2 {
		t.Fatalf("кусков %d, нужно хотя бы два", len(chunks))
	}
	for i, c := range chunks[1:] {
		if c.Flags&FlagCode == 0 {
			t.Errorf("кусок %d внутри блока кода не помечен как код", i+1)
		}
	}
}

// Пустой и почти пустой файл не дают кусков, а не падают.
func TestSplitTextEmpty(t *testing.T) {
	if got := SplitText(lineParts(""), DefaultChunkOpts()); len(got) != 0 {
		t.Errorf("из пустого файла вышло %d кусков", len(got))
	}
	if got := SplitText(lineParts("\n\n   \n\n"), DefaultChunkOpts()); len(got) != 0 {
		t.Errorf("из пробелов вышло %d кусков", len(got))
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}
