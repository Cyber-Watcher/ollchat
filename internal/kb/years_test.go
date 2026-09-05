package kb

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestYearsFromIndexAndRefresh — год попадает в реестр при индексации, а книге,
// собранной до появления этого поля, его проставляет отдельный проход.
func TestYearsFromIndexAndRefresh(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeBook(t, books, "Учебник по Go 2019.pdf", longPage("goroutines and channels"))
	makeBook(t, books, "Свежая книга 2026.pdf", longPage("kubernetes deployment"))

	c, err := base.Create("test", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{Workers: 2}, nil); err != nil {
		t.Fatal(err)
	}

	years := map[string]int{}
	for _, b := range c.Books() {
		years[b.Title] = b.Year
		if b.Year > 0 && b.YearSrc == "" {
			t.Fatalf("год без источника: %+v", b)
		}
	}
	if years["Учебник по Go 2019.pdf"] != 2019 || years["Свежая книга 2026.pdf"] != 2026 {
		t.Fatalf("год не проставился при индексации: %+v", years)
	}

	// Книга из старого реестра: год стёрт, как у собранных до появления поля.
	for _, b := range c.Books() {
		rec := b
		rec.Year, rec.YearSrc = 0, ""
		if err := c.appendDoc(rec); err != nil {
			t.Fatal(err)
		}
	}
	res, err := c.RefreshYears(context.Background(), 64<<20, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Found != 2 || res.Named != 2 {
		t.Fatalf("проход не проставил годы из имён: %+v", res)
	}
	// Имени файла хватило — открывать книги не понадобилось.
	if res.Opened != 0 {
		t.Fatalf("книги открывались зря: %+v", res)
	}
	for _, b := range c.Books() {
		if b.Year == 0 {
			t.Fatalf("после прохода книга без года: %s", b.Title)
		}
	}

	// Год виден в выдаче поиска — иначе модели не на что опереться.
	hits, err := c.Search("goroutines channels", DefaultSearchOpts())
	if err != nil || len(hits) == 0 {
		t.Fatalf("поиск ничего не нашёл: %v", err)
	}
	if hits[0].Year == 0 {
		t.Fatalf("в выдаче нет года книги: %+v", hits[0])
	}
}

// TestYearSpan — оговорка одна на выдачу и появляется только по старой книге.
func TestYearSpan(t *testing.T) {
	now := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)

	from, to, note := YearSpan([]Result{{Year: 2019}, {Year: 2026}, {Year: 0}}, now)
	if from != 2019 || to != 2026 {
		t.Fatalf("диапазон лет: %d–%d", from, to)
	}
	if !strings.Contains(note, "7 лет") {
		t.Fatalf("оговорка по самой старой книге: %q", note)
	}

	if _, _, note := YearSpan([]Result{{Year: 2025}, {Year: 2026}}, now); note != "" {
		t.Fatalf("оговорка на свежих книгах: %q", note)
	}
	if from, to, note := YearSpan([]Result{{Year: 0}}, now); from != 0 || to != 0 || note != "" {
		t.Fatalf("книги без годов дали оговорку: %d %d %q", from, to, note)
	}
}
