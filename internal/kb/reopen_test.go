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
	if _, err := w.Sync(context.Background(), IndexOpts{}, nil); err != nil {
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

// Замок, чужой файл и временный файл атомарной записи коллекцию изменённой
// не делают.
//
// В отпечатке было время правки самого каталога, а его меняет создание
// и удаление любого файла: служба перечитывала коллекцию целиком — с сотнями
// мегабайт векторов — от каждой поставленной и снятой блокировки.
func TestStaleIgnoresLockAndForeignFiles(t *testing.T) {
	base, coll, _ := mergeFixture(t)
	reader, err := base.Open(coll.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.lock(); err != nil {
		t.Fatal(err)
	}
	reader.unlock()
	if err := os.WriteFile(filepath.Join(reader.Dir(), "заметки.txt"), []byte("своё"), 0o644); err != nil {
		t.Fatal(err)
	}
	if reader.Stale() {
		t.Fatal("замок и чужой файл объявили коллекцию изменённой")
	}
	if again, _ := base.Open(coll.Name()); again != reader {
		t.Fatal("коллекция перечитана, хотя её данные не менялись")
	}
}

// Волны счёта смыслов, идущего в другом процессе, коллекцию не перечитывают:
// новые векторы подхватываются один раз, когда счёт закончится.
func TestStaleWaitsForVectorsToSettle(t *testing.T) {
	base, coll, _ := embedFixture(t)
	if _, err := coll.Embed(context.Background(), newFakeEmbedder(64), EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	reader, err := base.Open(coll.Name())
	if err != nil {
		t.Fatal(err)
	}
	if reader.Stale() {
		t.Fatal("подготовка: коллекция изменена сразу после открытия")
	}

	// «Другой процесс» считает смыслы: замок стоит, паспорт векторов
	// переписан очередной волной.
	lock := filepath.Join(reader.Dir(), lockMark)
	if err := placeMarker(lock); err != nil {
		t.Fatal(err)
	}
	_, metaPath := vecPaths(reader.Dir())
	var vm VecMeta
	if err := readJSON(metaPath, &vm); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(metaPath, vm); err != nil {
		t.Fatal(err)
	}
	if reader.Stale() {
		t.Fatal("волна векторов под живым замком заставила перечитать коллекцию")
	}

	// Счёт закончился — замок снят: теперь перечитать надо.
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if !reader.Stale() {
		t.Fatal("досчитанные векторы не подхватываются после снятия замка")
	}
}

// Перезапись указателей кусков (проход --kb-flag-toc) видна читателю:
// без времени каталога её выдаёт только chunks.idx в отпечатке.
func TestStaleSeesChunkIndexRewrite(t *testing.T) {
	base, coll, _ := mergeFixture(t)
	reader, err := base.Open(coll.Name())
	if err != nil {
		t.Fatal(err)
	}
	idx := filepath.Join(reader.Dir(), "chunks.idx")
	raw, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomicForTest(idx, raw); err != nil {
		t.Fatal(err)
	}
	if !reader.Stale() {
		t.Fatal("переписанные указатели кусков не замечены")
	}
}

// writeFileAtomicForTest — подмена файла переименованием, как у fsx.WriteFileAtomic.
func writeFileAtomicForTest(path string, data []byte) error {
	tmp := path + ".tmp-test"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
