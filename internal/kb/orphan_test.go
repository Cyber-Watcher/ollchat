package kb

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Куски перечитанной книги не должны попадать в выдачу безымянными.
//
// Перечитывание заменяет запись книги по пути: старая запись исчезает,
// а её куски физически остаются в хранилище до уплотнения. Пометки «удалено»
// при этом не появляется — помечать нечего, книга-то на месте.
//
// Отбор живых книг в Search строился **только если есть хоть одна помеченная
// удалённой**. У коллекции без таких пометок отбора не было вовсе, и осиротевшие
// куски проходили в выдачу: имя книги пустое, путь пустой, сослаться не на что.
func TestSearchSkipsOrphanChunks(t *testing.T) {
	base, books := newBase(t)
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	path := makeBook(t, books, "книга.pdf", longPage("obsoletewordonlyhere zzqq unique marker"))

	c, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	// Книгу переписали на диске и перечитали: запись заменится по пути.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "книга.pdf", longPage("kubernetes deployment docker"),
		longPage("goroutines and channels"))
	if _, err := c.Reindex(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(c.DeletedBooks()) != 0 {
		t.Skip("появились пометки удаления — случай не тот")
	}

	// Осиротевшие куски должны были появиться — иначе проверять нечего.
	acc := 0
	for _, b := range c.Books() {
		acc += b.Chunks
	}
	t.Logf("в хранилище %d кусков, у книг числится %d, помечено удалённых книг %d",
		c.Stats().Chunks, acc, len(c.DeletedBooks()))
	if c.Stats().Chunks <= acc {
		t.Skip("сирот не появилось — случай не воспроизвёлся")
	}

	hits, err := c.Search("obsoletewordonlyhere zzqq", DefaultSearchOpts())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("найдено %d кусков по слову, которого в живой книге нет", len(hits))
	for _, h := range hits {
		if h.Book == "" || h.Path == "" {
			t.Errorf("в выдаче безымянный кусок: id=%q книга=%q путь=%q текст=%.40s",
				h.ID, h.Book, h.Path, strings.TrimSpace(h.Text))
		}
	}
}
