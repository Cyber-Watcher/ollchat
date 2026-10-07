package kb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ссылка «книга, номер куска» разрешается по нынешнему хранилищу, а не по тому,
// что было при первом обращении.
//
// Отображение ссылок строилось один раз на всю жизнь объекта коллекции. После
// доливки в том же процессе новые книги по ссылке не находились вовсе, а после
// уплотнения сквозные номера сдвигались, и ChunkByRef отдавал кусок чужой книги
// (аудит 07.10.2026). Граф понятий разрешает свои ссылки ровно так.
func TestChunkByRefFollowsAddAndMerge(t *testing.T) {
	base, root := newBase(t)
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "a.pdf", longPage("alpha goroutines"), longPage("alpha channels"))
	coll, err := base.Create("refs", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := coll.Add(ctx, []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := coll.ChunkByRef(1, 0); !ok { // отображение построено
		t.Fatal("кусок первой книги не нашёлся по ссылке")
	}

	// Доливка: в отображении, построенном до неё, второй книги нет.
	drop := makeBook(t, books, "b.pdf", longPage("bravo kubernetes"), longPage("bravo pods"),
		longPage("bravo services"))
	makeBook(t, books, "c.pdf", longPage("charlie postgres"), longPage("charlie indexes"))
	// Один разборщик — книги ложатся в хранилище по порядку путей: b, затем c,
	// и уплотнение без b обязано сдвинуть сквозные номера c.
	if _, err := coll.Add(ctx, []string{books}, IndexOpts{Workers: 1}, nil); err != nil {
		t.Fatal(err)
	}
	ids := map[string]uint32{}
	for _, b := range coll.Books() {
		ids[filepath.Base(b.Path)] = b.ID
	}
	check := func(stage, file, word string) {
		t.Helper()
		doc := ids[file]
		info, ok := coll.ChunkByRef(doc, 0)
		if !ok {
			t.Fatalf("%s: кусок %d#0 по ссылке не нашёлся", stage, doc)
		}
		if info.Doc != doc || info.Ord != 0 || !strings.Contains(info.Text, word) {
			t.Fatalf("%s: по ссылке %d#0 отдан чужой кусок %d#%d: %.40q", stage, doc, info.Doc, info.Ord, info.Text)
		}
	}
	check("после доливки", "b.pdf", "bravo")
	check("после доливки", "c.pdf", "charlie")

	// Уплотнение без книги b.
	if err := coll.Forget(drop); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Merge(ctx, MergeOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	check("после уплотнения", "c.pdf", "charlie")
	check("после уплотнения", "a.pdf", "alpha")
}
