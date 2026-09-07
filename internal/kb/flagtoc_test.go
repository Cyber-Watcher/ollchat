package kb

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// tocPage — страница, похожая на оглавление: строки с номерами страниц.
func tocPage() string {
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		b.WriteString("Chapter section about kubernetes networking and storage ")
		b.WriteString(strings.Repeat("x", 3))
		b.WriteString("  ")
		b.WriteString(strconv.Itoa(100 + i))
		b.WriteString("\n")
	}
	return b.String()
}

// Нарезка ставит признак оглавлению, а проход по индексу его находит,
// повторный проход ничего не меняет, сухой — не пишет.
func TestFlagTOCAtIndexingAndPass(t *testing.T) {
	base, root := newBase(t)
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	// Текстовая книга: она режется по строкам, и переносы в куске остаются —
	// как у настоящих PDF с оглавлением. Тестовый PDF из makeBook кладёт
	// страницу одной строкой, и оглавлению в нём неоткуда взяться.
	body := tocPage() + "\n" + longPage("kubernetes pods and deployments") + "\n" + longPage("kubernetes services") + "\n"
	if err := os.WriteFile(filepath.Join(books, "k8s.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	coll, err := base.Create("lib", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	var tocChunks, total int
	if err := coll.EachChunk(ChunkFilter{}, func(ci ChunkInfo) error {
		total++
		if ci.TOC {
			tocChunks++
			if !LooksLikeTOC(ci.Text) {
				t.Errorf("признак стоит на куске, не похожем на оглавление: %.60q", ci.Text)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if tocChunks == 0 {
		t.Fatalf("нарезка не поставила признак ни одному куску из %d", total)
	}

	// Проход по уже помеченной коллекции: ничего не меняется.
	res, err := coll.FlagTOC(context.Background(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Flagged != tocChunks || res.Changed != 0 || res.Was != tocChunks {
		t.Fatalf("повторный проход: %+v при %d помеченных", res, tocChunks)
	}

	// Снимаем признаки руками — проход обязан их вернуть; сухой — только посчитать.
	idx := filepath.Join(coll.Dir(), "chunks.idx")
	raw, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(raw)/chunkRecSize; i++ {
		r := decodeRec(raw[i*chunkRecSize:])
		r.Flags &^= uint16(FlagTOC)
		r.encode(raw[i*chunkRecSize:])
	}
	if err := os.WriteFile(idx, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range coll.store.recs {
		coll.store.recs[i].Flags &^= uint16(FlagTOC)
	}
	dry, err := coll.FlagTOC(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Flagged != tocChunks || dry.Changed != tocChunks || dry.Was != 0 {
		t.Fatalf("сухой проход: %+v", dry)
	}
	if again, _ := os.ReadFile(idx); string(again) != string(raw) {
		t.Fatal("сухой проход переписал индекс")
	}
	wet, err := coll.FlagTOC(context.Background(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if wet.Changed != tocChunks {
		t.Fatalf("проход не вернул признаки: %+v", wet)
	}
	after, _ := os.ReadFile(idx)
	if len(after) != len(raw) {
		t.Fatalf("индекс изменил длину: %d → %d", len(raw), len(after))
	}
	n := 0
	for i := 0; i < len(after)/chunkRecSize; i++ {
		if decodeRec(after[i*chunkRecSize:]).Flags&uint16(FlagTOC) != 0 {
			n++
		}
	}
	if n != tocChunks {
		t.Fatalf("в индексе помечено %d, ожидалось %d", n, tocChunks)
	}

	// Помеченный кусок поиск не выдаёт.
	hits, err := coll.Search("kubernetes networking storage", SearchOpts{TopK: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if LooksLikeTOC(h.Text) {
			t.Fatalf("оглавление в выдаче: %.60q", h.Text)
		}
	}
}
