package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/document"
)

// Извлечение картинок ограничено тем, что покажут или перечислят. Раньше
// и показ, и объяснение «показывать нечего» доставали ВСЕ картинки документа
// в память — ради четырёх показанных и двадцати названных меток.
func TestViewImageExtractionIsBounded(t *testing.T) {
	prev := documentImages
	t.Cleanup(func() { documentImages = prev })
	var seen []document.ImageOptions
	documentImages = func(_ string, _ int64, opt document.ImageOptions) ([]document.Image, error) {
		seen = append(seen, opt)
		return nil, nil // пусто — заодно пройдёт и объяснение, почему пусто
	}

	reg, root := newTestRegistry(t)
	if err := os.WriteFile(filepath.Join(root, "doc.pdf"), pdfWithImage(), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args map[string]any
		want int // предел первого извлечения
	}{
		{map[string]any{"path": "doc.pdf"}, maxViewImages},
		{map[string]any{"path": "doc.pdf", "limit": 2.0}, 2},
		{map[string]any{"path": "doc.pdf", "page": 7.0}, maxViewImages},
		{map[string]any{"path": "doc.pdf", "figure": "5"}, maxViewImages},
		{map[string]any{"path": "doc.pdf", "figure": "3.2"}, 2},
		{map[string]any{"path": "doc.pdf", "figure": "3.999"}, maxFigureIndex},
	}
	for _, c := range cases {
		seen = nil
		plan, err := reg.Plan(NameViewImage, c.args)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if _, err := plan.Run(context.Background()); err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if len(seen) != 2 {
			t.Fatalf("%v: извлечений %d, ожидалось 2 (показ и объяснение)", c.args, len(seen))
		}
		if seen[0].MaxCount != c.want {
			t.Errorf("%v: предел показа %d, ожидалось %d", c.args, seen[0].MaxCount, c.want)
		}
		if seen[1].MaxCount != maxListedImages+1 {
			t.Errorf("%v: предел объяснения %d, ожидалось %d", c.args, seen[1].MaxCount, maxListedImages+1)
		}
	}
}
