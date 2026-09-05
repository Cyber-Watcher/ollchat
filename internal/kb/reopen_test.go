package kb

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Долгоживущий читатель обязан увидеть книгу, доложенную в коллекцию другим
// процессом. Именно этого не делала служба ollmcp: девять часов отдавала
// состояние на момент своего запуска.
func TestOpenSeesExternalReindex(t *testing.T) {
	base, root := newBase(t)
	defer base.Close()

	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "go.pdf", longPage("goroutines and channels"))

	coll, err := base.Create("lib", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	// Каталог запоминается в коллекции — иначе Sync не знает, что сверять.
	if err := coll.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	// Читатель открыл коллекцию и держит её.
	reader, err := base.Open("lib")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reader.Books()); got != 1 {
		t.Fatalf("книг у читателя %d, ожидалась одна", got)
	}

	// «Другой процесс» дописал книгу. Своя база, свои описатели файлов —
	// ровно как у отдельно запущенной службы.
	writerBase, err := OpenBase(base.Dir())
	if err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "rust.pdf", longPage("ownership and borrowing"))
	w, err := writerBase.Open("lib")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Sync(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	writerBase.Close()

	// Файловые системы хранят время с точностью до наносекунд, но проверка
	// опирается ещё и на размер с identity — ждать не нужно.
	again, err := base.Open("lib")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(again.Books()); got != 2 {
		t.Fatalf("после доливки снаружи читатель видит %d книг, ожидалось две", got)
	}
	if !found(t, again, "ownership") {
		t.Error("новая книга не ищется")
	}
}

// Уплотнение подменяет каталог коллекции целиком — это тоже должно замечаться.
func TestOpenSeesExternalMerge(t *testing.T) {
	base, coll, _ := mergeFixture(t)
	defer base.Close()
	name := coll.Name()

	before, err := base.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	chunksBefore := before.ChunkCount()

	writerBase, err := OpenBase(base.Dir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := writerBase.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Merge(context.Background(), MergeOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	writerBase.Close()

	after, err := base.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	if after.ChunkCount() == chunksBefore {
		t.Fatalf("после уплотнения снаружи читатель видит прежние %d кусков", chunksBefore)
	}
}

// Нетронутая коллекция не перечитывается: иначе каждый запрос к библиотеке
// в 463 МБ стоил бы полной загрузки индекса.
func TestOpenKeepsUnchangedCollection(t *testing.T) {
	base, coll, _ := mergeFixture(t)
	defer base.Close()

	first, err := base.Open(coll.Name())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	second, err := base.Open(coll.Name())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("коллекция перечитана, хотя на диске ничего не менялось")
	}
	if first.Stale() {
		t.Error("нетронутая коллекция считается изменённой")
	}
}
