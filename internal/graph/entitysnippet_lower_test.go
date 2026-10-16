package graph

import (
	"strings"
	"testing"
)

// Аудит 16.10.2026, находка 8: NameAt считал сдвиг в strings.ToLower(text), а
// EntitySnippet резал им исходный text; «Ⱥ» (2 байта) → «ⱥ» (3 байта) выводил
// срез за конец текста (паника), «İ» → «i» сдвигал выдержку мимо имени.
func TestEntitySnippetSurvivesCaseLengthChange(t *testing.T) {
	for _, head := range []string{strings.Repeat("Ⱥ", 300), strings.Repeat("İ", 300), strings.Repeat("K", 200)} {
		text := head + " PostgreSQL хранит данные в таблицах. " + strings.Repeat("ещё текст. ", 20)
		out := EntitySnippet(text, []string{"PostgreSQL"}, 120)
		if !strings.Contains(out, "PostgreSQL") {
			t.Errorf("голова %q…: выдержка без имени понятия: %q", head[:4], out)
		}
	}
	if at := NameAt("Ⱥ Ⱥ postgresql", []string{"PostgreSQL"}); at != 4 {
		t.Errorf("NameAt по рунам: %d, ждали 4", at)
	}
	if at := NameAt("a good algorithm", []string{"Go"}); at != -1 {
		t.Errorf("«Go» внутри «good» найден: %d", at)
	}
}
