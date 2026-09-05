package kb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Каждый выход из Add обязан сказать «готово».
//
// Строка хода работы в командной строке живёт по таймеру и гаснет только
// по Done. Молчаливый выход оставлял её тикающей до конца работы программы:
// она раз в секунду переписывала своё место на экране, затирая всё, что
// печаталось следом, — а следом печатается итог и список добавленных книг.
// Замер 30.08.2026: на `--kb-refresh books`, где все найденные книги оказались
// повторами, брошенная строка «обход» мельтешила со строкой счёта смыслов.
func TestAddAlwaysReportsDone(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, c *Collection, books string)
	}{
		{"нечего индексировать", func(t *testing.T, c *Collection, books string) {
			os.MkdirAll(books, 0o755) // пустой каталог: collect не найдёт ничего
		}},
		{"всё оказалось повторами", func(t *testing.T, c *Collection, books string) {
			os.MkdirAll(books, 0o755)
			makeBook(t, books, "one.pdf", longPage("goroutines and channels"))
			if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
				t.Fatal(err)
			}
			// Та же книга под другим именем: отсеется сверкой по содержимому.
			data, err := os.ReadFile(filepath.Join(books, "one.pdf"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(books, "copy.pdf"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(books, "one.pdf")); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, books := newBase(t)
			c, err := base.Create("test", "")
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(t, c, books)

			done := 0
			_, err = c.Add(context.Background(), []string{books}, IndexOpts{},
				func(p Progress) {
					if p.Done {
						done++
					}
				})
			if err != nil {
				t.Fatal(err)
			}
			if done != 1 {
				t.Fatalf("сообщений «готово» %d, ожидалось ровно одно", done)
			}
		})
	}
}

// До долгой работы Add сообщает, какие книги он собирается разбирать.
//
// Человек, положивший книги в каталог, спрашивает раньше, чем «что
// получилось»: «нашлись ли они вообще». Список добавленного отвечает на это
// только через минуты.
func TestAddAnnouncesQueue(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeBook(t, books, "one.pdf", longPage("goroutines and channels"))
	makeBook(t, books, "two.pdf", longPage("kubernetes deployment"))

	c, err := base.Create("test", "")
	if err != nil {
		t.Fatal(err)
	}

	var queue []string
	var afterExtract bool
	_, err = c.Add(context.Background(), []string{books}, IndexOpts{}, func(p Progress) {
		if p.Phase == "извлечение" {
			afterExtract = true
		}
		if len(p.Files) > 0 {
			if afterExtract {
				t.Error("список к разбору пришёл после начала разбора — он нужен до него")
			}
			queue = append(queue, p.Files...)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 2 {
		t.Fatalf("в списке к разбору %d книг, ожидалось 2: %v", len(queue), queue)
	}
	for _, want := range []string{"one.pdf", "two.pdf"} {
		found := false
		for _, got := range queue {
			if strings.HasSuffix(got, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("книги %s нет в списке к разбору: %v", want, queue)
		}
	}
}

// Sync убирает пропавшую книгу любого вида, а не только прочитанную.
//
// **Замер 30.08.2026 на живой библиотеке.** Пять книг были стёрты с диска,
// `--kb-sync` запускали не раз, а доктор продолжал показывать их
// «числящимися в коллекции». Все пять оказались видом `scan` с нулевым
// номером: отбор `d.Kind != BookOK` пропускал их мимо, то есть команда,
// созданная ровно для этого случая, не умела его вовсе. А чаще прочих
// с диска стирают как раз сканы — узнав, что толку от них нет.
func TestSyncRemovesVanishedScans(t *testing.T) {
	base, books := newBase(t)
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "живая.pdf", longPage("goroutines and channels"))
	// Скан: страницы есть, текста на них нет.
	scan := makeBook(t, books, "скан.pdf", "", "")

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

	var rec BookRec
	for _, b := range c.LiveBooks() {
		if filepath.Base(b.Path) == "скан.pdf" {
			rec = b
		}
	}
	if rec.Path == "" {
		t.Skip("скан не попал в коллекцию — проверять нечего")
	}
	if rec.Kind == BookOK {
		t.Skip("книга прочиталась как обычная — случай не тот")
	}

	// Владелец стёр скан с диска и запустил сверку.
	if err := os.Remove(scan); err != nil {
		t.Fatal(err)
	}
	res, err := c.Sync(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 {
		t.Errorf("убрано книг %d, ожидалась одна", res.Removed)
	}
	for _, b := range c.LiveBooks() {
		if filepath.Base(b.Path) == "скан.pdf" {
			t.Fatal("после сверки пропавший скан всё ещё числится в коллекции")
		}
	}
	if out := Doctor(c, DoctorOpts{}); strings.Contains(out, "скан.pdf") {
		t.Errorf("доктор всё ещё показывает убранный скан:\n%s", out)
	}
}
