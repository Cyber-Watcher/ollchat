package kb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// syncFixture — коллекция из двух книг с записанным корнем, чтобы работала
// сверка (Sync).
func syncFixture(t *testing.T) (*Base, *Collection, string) {
	t.Helper()
	base, root := newBase(t)
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "guide.pdf", longPage("alphaversion of the guide"), longPage("alphaversion details"))
	makeBook(t, books, "other.pdf", longPage("kubernetes pods"), longPage("kubernetes services"))
	coll, err := base.Create("docs", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := coll.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	return base, coll, books
}

// bookID — номер книги по имени файла.
func bookID(t *testing.T, c *Collection, file string) uint32 {
	t.Helper()
	for _, b := range c.Books() {
		if filepath.Base(b.Path) == file {
			return b.ID
		}
	}
	t.Fatalf("книги %s нет в реестре", file)
	return 0
}

// Изменённый файл, перечитанный сверкой, вытесняет свою прежнюю версию.
//
// --kb-sync перечитывал изменённый файл под новым номером, а прежний номер
// удалённым не помечал, и поиск по коллекции без единого удаления отбора
// живых книг не ставил вовсе: старый текст находился, с пустыми названием
// и путём. Сильнее всего это било по документации, которую доливают после
// каждой правки.
func TestSyncRetiresPreviousVersion(t *testing.T) {
	_, coll, books := syncFixture(t)
	oldID := bookID(t, coll, "guide.pdf")
	if !found(t, coll, "alphaversion") {
		t.Fatal("первая версия не находится")
	}

	// Новая версия того же файла: другой текст, другой размер, позже время.
	path := makeBook(t, books, "guide.pdf", longPage("betaversion rewritten guide"), longPage("betaversion more"),
		longPage("betaversion appendix"))
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	res, err := coll.Sync(context.Background(), IndexOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("перечитано книг %d, ожидалась одна", res.Added)
	}

	if hits, _ := coll.Search("alphaversion", DefaultSearchOpts()); len(hits) > 0 {
		t.Fatalf("прежняя версия книги осталась в выдаче: %q из %q", hits[0].Snippet, hits[0].Path)
	}
	if !found(t, coll, "betaversion") {
		t.Fatal("новая версия не находится")
	}
	if !coll.isDeleted(oldID) {
		t.Errorf("прежний номер %d не помечен удалённым — уплотнение не освободит его куски", oldID)
	}
	if newID := bookID(t, coll, "guide.pdf"); newID == oldID {
		t.Errorf("перечитанная книга сохранила прежний номер %d", newID)
	}
}

// Ничьи куски — без записи в реестре и без пометки удалённых — поиском
// не выдаются, даже когда удалённых в коллекции нет вовсе.
//
// Такие куски уже лежат в живых коллекциях: их оставлял --kb-sync до
// 07.10.2026 и оставляет обрыв записи. Отбор живых книг ставился только при
// непустом deleted.ids, и в коллекции без удалений они находились.
func TestSearchSkipsChunksOutsideRegistry(t *testing.T) {
	_, coll, _ := syncFixture(t)
	if len(coll.DeletedBooks()) != 0 {
		t.Fatal("в коллекции не должно быть удалённых")
	}
	w, err := CreateWriter(coll.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(999, chunksOf("сиротский кусок orphanword без записи в реестре")); err != nil {
		t.Fatal(err)
	}
	state, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if err := coll.journal(state, 999); err != nil {
		t.Fatal(err)
	}
	if err := coll.buildSegment(context.Background(), func(Progress) {}); err != nil {
		t.Fatal(err)
	}

	if hits, _ := coll.Search("orphanword", DefaultSearchOpts()); len(hits) > 0 {
		t.Fatalf("ничий кусок выдан поиском: %q", hits[0].Snippet)
	}
	if !found(t, coll, "kubernetes") {
		t.Fatal("живые книги перестали находиться")
	}
	if !strings.Contains(strings.Join(texts0(t, coll), " "), "orphanword") {
		t.Fatal("подготовка: ничий кусок не попал в хранилище")
	}
}

// Номер книги не выдаётся повторно, даже если meta.json отстал от данных.
//
// NextDoc живёт только в meta.json и пишется после кусков и реестра. Обрыв
// между ними оставлял счётчик позади, следующая книга получала уже выданный
// номер — и выдача одной книги приписывалась другой.
func TestOpenReconcilesNextDoc(t *testing.T) {
	base, root := newBase(t)
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "a.pdf", longPage("alphaword first book"))
	makeBook(t, books, "b.pdf", longPage("bravoword second book"))
	coll, err := base.Create("ids", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := coll.Add(ctx, []string{books}, IndexOpts{Workers: 1}, nil); err != nil {
		t.Fatal(err)
	}

	// Обрыв: куски и запись книги b на диске, а счётчик — как до неё.
	meta := coll.Meta()
	meta.NextDoc = bookID(t, coll, "b.pdf")
	if err := writeJSON(filepath.Join(coll.Dir(), "meta.json"), meta); err != nil {
		t.Fatal(err)
	}
	base.Close()

	base2, err := OpenBase(base.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer base2.Close()
	c2, err := base2.Open("ids")
	if err != nil {
		t.Fatal(err)
	}
	makeBook(t, books, "c.pdf", longPage("charlieword third book"))
	if _, err := c2.Add(ctx, []string{books}, IndexOpts{Workers: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if b, c := bookID(t, c2, "b.pdf"), bookID(t, c2, "c.pdf"); b == c {
		t.Fatalf("номер %d выдан двум книгам", c)
	}
	hits, err := c2.Search("charlieword", DefaultSearchOpts())
	if err != nil || len(hits) == 0 {
		t.Fatalf("третья книга не находится: %v", err)
	}
	if got := filepath.Base(hits[0].Path); got != "c.pdf" {
		t.Fatalf("выдача третьей книги приписана %s", got)
	}
}

// Обрывок книги, прерванной первой после уплотнения, откатывается.
//
// Уплотнение начинает журнал коммитов заново, а пустой журнал значил
// «откатывать не к чему», хотя состояние хранилища лежит в meta.json.
func TestTornTailAfterMergeRolledBack(t *testing.T) {
	base, coll, drop := mergeFixture(t)
	if _, err := coll.Merge(context.Background(), MergeOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	// Паспорт в памяти — из нового каталога, а не прежний: его первая же
	// запись вернула бы на диск состояние хранилища до уплотнения.
	var disk Meta
	if err := readJSON(filepath.Join(coll.Dir(), "meta.json"), &disk); err != nil {
		t.Fatal(err)
	}
	if mem := coll.Meta(); mem.State != disk.State || mem.NextSeg != disk.NextSeg {
		t.Fatalf("после уплотнения паспорт в памяти %+v/%d, в новом каталоге %+v/%d",
			mem.State, mem.NextSeg, disk.State, disk.NextSeg)
	}
	// Удалённая книга ушла и с диска — иначе доливка взяла бы её как новую.
	if err := os.Remove(drop); err != nil {
		t.Fatal(err)
	}
	before := coll.ChunkCount()

	// Обрыв: куски записаны, а ни журнал, ни реестр о них не знают.
	w, err := CreateWriter(coll.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(77, chunksOf("обрывок после уплотнения tornword")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	w.Close()

	books := filepath.Join(filepath.Dir(base.Dir()), "books", "books")
	makeBook(t, books, "fresh.pdf", longPage("freshword new material"))
	res, err := coll.Add(context.Background(), []string{books}, IndexOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("добавлено книг %d, ожидалась одна", res.Added)
	}
	for _, text := range texts0(t, coll) {
		if strings.Contains(text, "tornword") {
			t.Fatal("обрывок после уплотнения остался в хранилище")
		}
	}
	if got, want := coll.ChunkCount(), before+int(res.Chunks); got != want {
		t.Fatalf("кусков %d, ожидалось %d", got, want)
	}
}

// Прерванная переиндексация не теряет книгу.
//
// Reindex помечал книгу удалённой до доливки, и Esc, занятый замок или сбой
// посреди неё оставляли книгу удалённой навсегда: запись реестра цела, файл
// не менялся — и сверка её больше не трогала.
func TestReindexInterruptedKeepsBook(t *testing.T) {
	_, coll, books := syncFixture(t)
	guide := filepath.Join(books, "guide.pdf")
	oldID := bookID(t, coll, "guide.pdf")

	// Esc до начала работы.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := coll.Reindex(ctx, []string{guide}, IndexOpts{}, nil); err != nil {
		t.Fatalf("прерванная переиндексация вернула ошибку: %v", err)
	}
	if !found(t, coll, "alphaversion") || coll.isDeleted(oldID) {
		t.Fatal("после Esc книга пропала из выдачи")
	}

	// Коллекцию держит другой живой процесс: доливка не начнётся вовсе.
	lock := filepath.Join(coll.Dir(), lockMark)
	foreign := []byte(fmt.Sprintf("%d %s\n", os.Getppid(), time.Now().Format(time.RFC3339)))
	if err := os.WriteFile(lock, foreign, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Reindex(context.Background(), []string{guide}, IndexOpts{}, nil); err == nil {
		t.Fatal("переиндексация прошла под чужим замком")
	}
	if !found(t, coll, "alphaversion") || coll.isDeleted(oldID) {
		t.Fatal("после отказа по замку книга пропала из выдачи")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}

	// Дошедшая до конца — заменяет: новый номер жив, прежний удалён.
	res, err := coll.Reindex(context.Background(), []string{guide}, IndexOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("перечитано книг %d, ожидалась одна", res.Added)
	}
	newID := bookID(t, coll, "guide.pdf")
	if newID == oldID || !coll.isDeleted(oldID) || coll.isDeleted(newID) {
		t.Fatalf("после переиндексации номера: прежний %d (удалён %v), новый %d (удалён %v)",
			oldID, coll.isDeleted(oldID), newID, coll.isDeleted(newID))
	}
	hits, err := coll.Search("alphaversion", DefaultSearchOpts())
	if err != nil || len(hits) == 0 {
		t.Fatalf("перечитанная книга не находится: %v", err)
	}
	for _, h := range hits {
		if !strings.HasPrefix(h.ID, fmt.Sprintf("docs/%d#", newID)) {
			t.Fatalf("в выдаче кусок прежней версии %s", h.ID)
		}
	}
}

// texts0 — тексты всех кусков хранилища коллекции.
func texts0(t *testing.T, c *Collection) []string {
	t.Helper()
	var out []string
	for i := 0; i < c.ChunkCount(); i++ {
		m, err := c.ChunkTexts([]int{i})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m[i])
	}
	return out
}
