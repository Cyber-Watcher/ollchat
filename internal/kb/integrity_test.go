package kb

import (
	"context"
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
