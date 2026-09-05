package kb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mergeFixture собирает коллекцию из трёх книг и удаляет среднюю.
func mergeFixture(t *testing.T) (*Base, *Collection, string) {
	t.Helper()
	base, root := newBase(t)
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "go.pdf", longPage("goroutines and channels"), longPage("scheduler internals"))
	drop := makeBook(t, books, "perl.pdf", longPage("perl regular expressions"), longPage("perl modules"))
	makeBook(t, books, "k8s.pdf", longPage("kubernetes pods"), longPage("kubernetes deployments"))

	coll, err := base.Create("lib", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := coll.Forget(drop); err != nil {
		t.Fatal(err)
	}
	return base, coll, drop
}

func found(t *testing.T, c *Collection, query string) bool {
	t.Helper()
	res, err := c.Search(query, SearchOpts{TopK: 10})
	if err != nil {
		t.Fatal(err)
	}
	return len(res) > 0
}

// TestMergeDropsDeleted — главное свойство уплотнения: удалённое исчезает
// с диска, а всё остальное продолжает находиться.
func TestMergeDropsDeleted(t *testing.T) {
	_, coll, _ := mergeFixture(t)

	if found(t, coll, "perl") {
		t.Fatal("удалённая книга ищется до уплотнения — пометка не сработала")
	}
	before := coll.Stats()

	res, err := coll.Merge(context.Background(), MergeOpts{}, nil)
	if err != nil {
		t.Fatalf("уплотнение: %v", err)
	}
	if res.BooksDropped != 1 {
		t.Fatalf("выброшено книг %d, ожидалась 1", res.BooksDropped)
	}
	if res.ChunksAfter >= res.ChunksBefore {
		t.Fatalf("кусков не убавилось: было %d, стало %d", res.ChunksBefore, res.ChunksAfter)
	}
	if res.BytesAfter >= res.BytesBefore {
		t.Fatalf("места не освободилось: было %d, стало %d", res.BytesBefore, res.BytesAfter)
	}

	after := coll.Stats()
	if after.Segments != 1 {
		t.Fatalf("сегментов после слияния %d, ожидался 1", after.Segments)
	}
	// Stats.Books помеченных удалёнными уже не считает, поэтому число живых
	// книг не меняется — меняется счётчик удалённых и место на диске.
	if before.Deleted != 1 || after.Deleted != 0 {
		t.Fatalf("список удалённых: было %d, стало %d — ожидалось 1 и 0", before.Deleted, after.Deleted)
	}
	if after.Books != before.Books {
		t.Fatalf("живых книг стало %d вместо %d", after.Books, before.Books)
	}

	// Соседи по коллекции обязаны пережить уплотнение.
	for _, q := range []string{"goroutines", "kubernetes"} {
		if !found(t, coll, q) {
			t.Fatalf("после уплотнения не находится %q", q)
		}
	}
	if found(t, coll, "perl") {
		t.Fatal("удалённая книга воскресла после уплотнения")
	}
}

// TestMergeKeepsChunkIDs закрепляет обещание: ссылка вида «lib/3#7» переживает
// уплотнение. Ссылки на страницы книг уже разошлись по ответам модели,
// и ломать их нельзя.
func TestMergeKeepsChunkIDs(t *testing.T) {
	_, coll, _ := mergeFixture(t)

	hits, err := coll.Search("kubernetes", SearchOpts{TopK: 1})
	if err != nil || len(hits) == 0 {
		t.Fatalf("поиск до уплотнения: %v, найдено %d", err, len(hits))
	}
	id, page := hits[0].ID, hits[0].UnitFrom

	if _, err := coll.Merge(context.Background(), MergeOpts{}, nil); err != nil {
		t.Fatal(err)
	}

	around, err := coll.Around(id, 0)
	if err != nil {
		t.Fatalf("номер куска %q после уплотнения не читается: %v", id, err)
	}
	if len(around) == 0 {
		t.Fatalf("по номеру %q ничего не найдено", id)
	}
	if around[0].UnitFrom != page {
		t.Fatalf("страница уехала: было %d, стало %d", page, around[0].UnitFrom)
	}
}

// TestMergeCancelKeepsCollection — прерывание не должно оставлять от коллекции
// огрызок: либо прежняя целиком, либо новая целиком.
func TestMergeCancelKeepsCollection(t *testing.T) {
	base, coll, _ := mergeFixture(t)
	before := coll.Stats()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := coll.Merge(ctx, MergeOpts{}, nil)
	if err != nil {
		t.Fatalf("прерванное уплотнение вернуло ошибку: %v", err)
	}
	if !res.Canceled {
		t.Fatal("прерывание не отмечено")
	}
	if !found(t, coll, "goroutines") {
		t.Fatal("после прерывания коллекция перестала искать")
	}
	if got := coll.Stats(); got.Chunks != before.Chunks {
		t.Fatalf("куски изменились после прерывания: было %d, стало %d", before.Chunks, got.Chunks)
	}
	// Рабочий каталог за собой не оставляем.
	entries, _ := os.ReadDir(filepath.Join(base.Dir(), "collections"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("остался рабочий каталог %s", e.Name())
		}
	}
}

// TestMergeHiddenDirsAreNotCollections — отставленный каталог не должен
// показаться отдельной коллекцией.
func TestMergeHiddenDirsAreNotCollections(t *testing.T) {
	base, _, _ := mergeFixture(t)
	hidden := filepath.Join(base.Dir(), "collections", ".old-lib")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "meta.json"), []byte(`{"name":"lib"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	names, err := base.Names()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if strings.HasPrefix(n, ".") {
			t.Fatalf("рабочий каталог показан коллекцией: %v", names)
		}
	}
}

// TestPendingSeesNewAndMissing — сверка для kb.sync_on_start: считает, ничего
// не индексируя.
func TestPendingSeesNewAndMissing(t *testing.T) {
	base, root := newBase(t)
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "go.pdf", longPage("goroutines and channels"))
	gone := makeBook(t, books, "old.pdf", longPage("obsolete topic here"))

	coll, err := base.Create("lib", "")
	if err != nil {
		t.Fatal(err)
	}
	// Сверка опирается на записанные каталоги коллекции — без них сравнивать
	// не с чем, и Pending обязана молчать.
	if err := coll.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if ch, err := coll.Pending(); err != nil || ch.Any() {
		t.Fatalf("сразу после индексации есть изменения: %+v, %v", ch, err)
	}

	makeBook(t, books, "new.pdf", longPage("brand new material"))
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	ch, err := coll.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if ch.New != 1 || ch.Missing != 1 {
		t.Fatalf("сверка: новых %d, пропавших %d — ожидалось 1 и 1", ch.New, ch.Missing)
	}
	// Сверка обязана быть безобидной: индекс не трогается.
	if !found(t, coll, "goroutines") {
		t.Fatal("после сверки поиск сломался")
	}
	if got := coll.Stats(); got.Deleted != 0 {
		t.Fatalf("сверка сама пометила книги удалёнными: %d", got.Deleted)
	}
}

// Уплотнение коллекции, по которой собран граф понятий, отклоняется.
//
// Цена ошибки несимметрична: уплотнение освобождает десятки мегабайт,
// а пересборка графа стоит часов работы видеокарты. Правило, которое держится
// на памяти человека, однажды не сработает — поэтому отказ в коде.
func TestMergeRefusedWhenGraphExists(t *testing.T) {
	base, coll, _ := mergeFixture(t)
	defer base.Close()

	// Подделываем граф: важен сам факт наличия паспорта рядом с коллекцией.
	dir := filepath.Join(coll.Dir(), graphDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "graph.meta"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	if !coll.HasGraph() {
		t.Fatal("граф рядом с коллекцией не распознан")
	}

	_, err := coll.Merge(context.Background(), MergeOpts{}, nil)
	if err == nil {
		t.Fatal("уплотнение при собранном графе должно отклоняться")
	}
	for _, want := range []string{"граф", "уплотнять нельзя"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в отказе нет %q: %v", want, err)
		}
	}

	// Осознанное решение возможно, но только явным разрешением.
	if _, err := coll.Merge(context.Background(), MergeOpts{Force: true}, nil); err != nil {
		t.Fatalf("с явным разрешением уплотнение должно идти: %v", err)
	}
}

// Без графа уплотнение идёт как прежде — предохранитель не мешает обычной работе.
func TestMergeAllowedWithoutGraph(t *testing.T) {
	base, coll, _ := mergeFixture(t)
	defer base.Close()

	if coll.HasGraph() {
		t.Fatal("графа быть не должно")
	}
	if _, err := coll.Merge(context.Background(), MergeOpts{}, nil); err != nil {
		t.Fatalf("уплотнение без графа: %v", err)
	}
}
