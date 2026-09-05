package kb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeBook кладёт в каталог документ PDF с заданным текстом на страницах.
func makeBook(t *testing.T, dir, name string, pages ...string) string {
	t.Helper()
	stream := func(data string) string {
		return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(data), data)
	}
	var objs []string
	kids := ""
	objNo := 3
	var pageObjs, contentObjs []string
	for range pages {
		kids += fmt.Sprintf("%d 0 R ", objNo)
		objNo += 2
	}
	objs = append(objs,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d /Resources << /Font << /F1 %d 0 R >> >> >>",
			strings.TrimSpace(kids), len(pages), objNo))
	no := 3
	for _, text := range pages {
		pageObjs = append(pageObjs, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /Contents %d 0 R >>", no+1))
		esc := strings.NewReplacer("(", "\\(", ")", "\\)").Replace(text)
		contentObjs = append(contentObjs, stream("BT /F1 12 Tf 50 700 Td ("+esc+") Tj ET"))
		no += 2
	}
	for i := range pageObjs {
		objs = append(objs, pageObjs[i], contentObjs[i])
	}
	objs = append(objs, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	for i, body := range objs {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// longPage набирает страницу правдоподобного объёма.
func longPage(topic string) string {
	return topic + ". " + strings.Repeat("plain sentence of book text about the subject. ", 12)
}

func newBase(t *testing.T) (*Base, string) {
	t.Helper()
	dir := t.TempDir()
	base, err := OpenBase(filepath.Join(dir, "kb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	return base, filepath.Join(dir, "books")
}

func TestCollectionIndexAndSearch(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeBook(t, books, "one.pdf", longPage("goroutines and channels"), longPage("mutex and locking"))
	makeBook(t, books, "two.pdf", longPage("kubernetes deployment"), longPage("docker containers"))

	c, err := base.Create("test", "проверочная коллекция")
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Add(context.Background(), []string{books}, IndexOpts{Workers: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 {
		t.Fatalf("добавлено %d книг, ожидалось 2 (%+v)", res.Added, res)
	}

	hits, err := c.Search("mutex locking", DefaultSearchOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("поиск ничего не нашёл")
	}
	if !strings.Contains(hits[0].Text, "mutex") {
		t.Fatalf("найден не тот кусок: %.60s", hits[0].Text)
	}
	// Ссылка на источник должна быть полной: книга и страница.
	if hits[0].Book == "" || hits[0].UnitFrom == 0 {
		t.Fatalf("в результате нет ссылки на источник: %+v", hits[0])
	}
	if hits[0].ID == "" || !strings.HasPrefix(hits[0].ID, "test/") {
		t.Fatalf("нет номера куска для расширения цитаты: %q", hits[0].ID)
	}
}

// TestCollectionAppendIsCheap — главное свойство: доливка книг стоит ровно
// столько, сколько новых книг, и не трогает уже собранное.
func TestCollectionAppendIsCheap(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeBook(t, books, "one.pdf", longPage("goroutines and channels"))

	c, _ := base.Create("test", "")
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	// Повторный вызов на той же папке не должен делать ничего.
	res, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 {
		t.Fatalf("повторная индексация перечитала %d книг", res.Added)
	}

	// Доложили книгу — обработана только она.
	makeBook(t, books, "two.pdf", longPage("kubernetes deployment"))
	res, err = c.Add(context.Background(), []string{books}, IndexOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("после доливки обработано %d книг, ожидалась одна", res.Added)
	}
	// И найтись должны обе.
	for _, q := range []string{"goroutines", "kubernetes"} {
		hits, err := c.Search(q, DefaultSearchOpts())
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 {
			t.Fatalf("после доливки не находится %q", q)
		}
	}
	if st := c.Stats(); st.Segments != 2 {
		t.Fatalf("сегментов %d, ожидалось 2 — доливка обязана создавать новый", st.Segments)
	}
}

// TestCollectionForgetsRemovedBook — удалённая книга пропадает из выдачи.
func TestCollectionForgetsRemovedBook(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeBook(t, books, "one.pdf", longPage("goroutines and channels"))
	path := makeBook(t, books, "two.pdf", longPage("kubernetes deployment"))

	c, _ := base.Create("test", "")
	c.Add(context.Background(), []string{books}, IndexOpts{}, nil)
	c.AddRoots([]string{books})

	if hits, _ := c.Search("kubernetes", DefaultSearchOpts()); len(hits) == 0 {
		t.Fatal("книга не нашлась до удаления")
	}

	os.Remove(path)
	res, err := c.Sync(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 {
		t.Fatalf("помечено удалённых %d, ожидалась одна", res.Removed)
	}
	if hits, _ := c.Search("kubernetes", DefaultSearchOpts()); len(hits) != 0 {
		t.Fatalf("удалённая книга всё ещё в выдаче: %d совпадений", len(hits))
	}
	// А оставшаяся книга должна находиться по-прежнему.
	if hits, _ := c.Search("goroutines", DefaultSearchOpts()); len(hits) == 0 {
		t.Fatal("вместе с удалённой пропала и оставшаяся книга")
	}
}

// TestCollectionSurvivesInterruption — прерывание стоит одной книги, а всё
// записанное до неё остаётся целым и находимым.
//
// Обрыв воспроизводится честно: в хранилище дописываются куски книги, о которой
// не успели записать ни строку реестра, ни отметку в журнале. Ровно так
// выглядит падение или Ctrl+C посреди работы.
func TestCollectionSurvivesInterruption(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	for i := 0; i < 3; i++ {
		makeBook(t, books, fmt.Sprintf("book%d.pdf", i), longPage(fmt.Sprintf("topic%d unique", i)))
	}

	c, _ := base.Create("test", "")
	if _, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	before := c.Stats().Chunks

	// Обрыв: куски записаны, журнал о них не знает.
	w, err := CreateWriter(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(999, chunksOf("обрывок книги, на которой прервались", "второй обрывок")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	w.Close()

	// Продолжение работы обязано отбросить обрывок и добавить новую книгу.
	makeBook(t, books, "book3.pdf", longPage("topic3 unique"))
	res, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil)
	if err != nil {
		t.Fatalf("продолжение не удалось: %v", err)
	}
	if res.Added != 1 {
		t.Fatalf("добавлено %d книг, ожидалась одна", res.Added)
	}
	if got := c.Stats().Chunks; got <= before {
		t.Fatalf("кусков стало %d против %d — новая книга не записалась", got, before)
	}

	// Все четыре книги должны находиться, обрывок — нет.
	for i := 0; i < 4; i++ {
		q := fmt.Sprintf("topic%d", i)
		hits, err := c.Search(q, DefaultSearchOpts())
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 {
			t.Fatalf("книга %q потеряна", q)
		}
	}
	if hits, _ := c.Search("обрывок книги", DefaultSearchOpts()); len(hits) > 0 {
		t.Fatal("обрывок прерванной книги остался в индексе")
	}
}

// TestCollectionCancelledAddIsSafe — отмена до начала работы не должна портить
// коллекцию.
func TestCollectionCancelledAddIsSafe(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeBook(t, books, "one.pdf", longPage("goroutines and channels"))

	c, _ := base.Create("test", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Add(ctx, []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatalf("отменённая индексация вернула ошибку: %v", err)
	}
	// А следующая попытка должна отработать как ни в чём не бывало.
	res, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("после отмены добавлено %d книг, ожидалась одна", res.Added)
	}
}

// TestCollectionRecordsFailures — скан и битый файл должны попасть в реестр
// с причиной, а не исчезнуть молча.
func TestCollectionRecordsFailures(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	makeBook(t, books, "good.pdf", longPage("goroutines and channels"))
	os.WriteFile(filepath.Join(books, "broken.pdf"), []byte("%PDF-1.7\nмусор"), 0o644)

	c, _ := base.Create("test", "")
	res, err := c.Add(context.Background(), []string{books}, IndexOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("добавлено %d книг, ожидалась одна", res.Added)
	}
	if res.Scans+res.Errors+res.Skipped == 0 {
		t.Fatal("сбой на битом файле не учтён")
	}
	var found bool
	for _, b := range c.Books() {
		if strings.HasSuffix(b.Path, "broken.pdf") {
			found = true
			if b.Kind == BookOK || b.Err == "" {
				t.Fatalf("битый файл записан как исправный: %+v", b)
			}
		}
	}
	if !found {
		t.Fatal("битого файла нет в реестре — он исчез молча")
	}
}

// TestCollectionAroundExpandsQuote — модель просит расширить цитату, когда
// найденного мало.
func TestCollectionAroundExpandsQuote(t *testing.T) {
	base, books := newBase(t)
	os.MkdirAll(books, 0o755)
	var pages []string
	for i := 0; i < 8; i++ {
		pages = append(pages, longPage(fmt.Sprintf("section%d about scheduling", i)))
	}
	makeBook(t, books, "one.pdf", pages...)

	c, _ := base.Create("test", "")
	c.Add(context.Background(), []string{books}, IndexOpts{}, nil)

	hits, err := c.Search("section4", DefaultSearchOpts())
	if err != nil || len(hits) == 0 {
		t.Fatalf("поиск не нашёл: %v", err)
	}
	around, err := c.Around(hits[0].ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(around) < 2 {
		t.Fatalf("окружение куска не выдано: %d кусков", len(around))
	}
}

// TestBaseCreateOpenRemove — коллекции заводятся, открываются и удаляются.
func TestBaseCreateOpenRemove(t *testing.T) {
	base, _ := newBase(t)
	if _, err := base.Create("go", "книги по Go"); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Create("go", ""); err == nil {
		t.Fatal("повторное создание с тем же именем прошло")
	}
	names, _ := base.Names()
	if len(names) != 1 || names[0] != "go" {
		t.Fatalf("список коллекций: %v", names)
	}
	if _, err := base.Open("go"); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Open("нет-такой"); err == nil {
		t.Fatal("открылась несуществующая коллекция")
	}
	if err := base.Remove("go"); err != nil {
		t.Fatal(err)
	}
	if names, _ := base.Names(); len(names) != 0 {
		t.Fatalf("после удаления осталось: %v", names)
	}
}

func TestValidName(t *testing.T) {
	for _, bad := range []string{"", "с пробелом", "слэш/внутри", "..", strings.Repeat("a", 100)} {
		if err := ValidName(bad); err == nil {
			t.Errorf("имя %q принято, а не должно", bad)
		}
	}
	for _, good := range []string{"go", "csharp-2", "infosec_2026"} {
		if err := ValidName(good); err != nil {
			t.Errorf("имя %q отвергнуто: %v", good, err)
		}
	}
}

// TestBreakdownByFolder — разбивка коллекции по подпапкам.
//
// «Книг: 205» само по себе не говорит ничего: от состава библиотеки прямо
// зависит, чего ждать от поиска.
func TestBreakdownByFolder(t *testing.T) {
	base, root := newBase(t)
	books := filepath.Join(root, "books")
	for _, sub := range []string{"Go", "CSharp", "CSharp/deep"} {
		if err := os.MkdirAll(filepath.Join(books, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	makeBook(t, filepath.Join(books, "Go"), "gopl.pdf", longPage("goroutines and channels"))
	makeBook(t, filepath.Join(books, "CSharp"), "c1.pdf", longPage("linq and delegates"))
	makeBook(t, filepath.Join(books, "CSharp"), "c2.pdf", longPage("async and await"))
	// Вложенная папка обязана попасть в свой первый уровень, а не отдельной строкой.
	makeBook(t, filepath.Join(books, "CSharp/deep"), "c3.pdf", longPage("il and jit"))
	// Книга прямо в корне.
	makeBook(t, books, "loose.pdf", longPage("random topic"))

	coll, err := base.Create("lib", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := coll.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	got := map[string]int{}
	total := 0
	for _, f := range coll.Breakdown() {
		got[f.Folder] = f.Books
		total += f.Books
		if f.Chunks == 0 {
			t.Errorf("у папки %s нет кусков", f.Folder)
		}
	}
	want := map[string]int{"CSharp": 3, "Go": 1, ".": 1}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("папка %q: книг %d, ожидалось %d (вся разбивка: %v)", k, got[k], v, got)
		}
	}
	if total != coll.Stats().Books {
		t.Fatalf("разбивка даёт %d книг, Stats.Books — %d", total, coll.Stats().Books)
	}
	// Порядок — по убыванию числа книг: самое крупное сверху.
	if first := coll.Breakdown()[0].Folder; first != "CSharp" {
		t.Fatalf("первой идёт %q, ожидалась самая крупная папка", first)
	}
}
