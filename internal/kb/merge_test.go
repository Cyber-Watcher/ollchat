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

// Ничьи куски — книги без записи в реестре — уплотнение стирает, как и обещает
// его предпросмотр («кусков книг, перечитанных заново: записи о них уже нет»).
//
// До 07.10.2026 стирались только помеченные удалёнными: прежние версии книг,
// перечитанных --kb-sync, переезжали в новое хранилище при каждом уплотнении,
// и обещанное место не освобождалось.
func TestMergeDropsChunksOutsideRegistry(t *testing.T) {
	_, coll, _ := mergeFixture(t)
	// Прежняя версия книги, какой её оставлял --kb-sync до 07.10.2026: куски
	// записаны и закоммичены, а ни записи в реестре, ни пометки удаления нет.
	w, err := CreateWriter(coll.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(77, chunksOf("orphanword прежняя версия книги")); err != nil {
		t.Fatal(err)
	}
	st, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if err := coll.journal(st, 77); err != nil {
		t.Fatal(err)
	}
	if err := coll.reopenIndex(); err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, b := range coll.LiveBooks() {
		live += b.Chunks
	}

	res, err := coll.Merge(context.Background(), MergeOpts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ChunksAfter != live {
		t.Fatalf("после уплотнения кусков %d, у живых книг реестра %d", res.ChunksAfter, live)
	}
	for _, text := range texts0(t, coll) {
		if strings.Contains(text, "orphanword") {
			t.Fatal("ничей кусок пережил уплотнение")
		}
	}
	for _, q := range []string{"goroutines", "kubernetes"} {
		if !found(t, coll, q) {
			t.Fatalf("после уплотнения не находится живая книга по %q", q)
		}
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

// fakeGraph кладёт в коллекцию каталог графа с паспортом и журналом.
// Содержимое возвращается: по нему проверяется, что граф пережил уплотнение
// байт в байт.
func fakeGraph(t *testing.T, collDir, name string) map[string]string {
	t.Helper()
	dir := filepath.Join(collDir, name)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"graph.meta":     `{"version":2,"chunks":7}`,
		"edges.log":      "связи, которые стоили недель работы видеокарты",
		"sub/notes.json": `{"kept":true}`,
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

// sameGraph проверяет, что каталог графа цел: все файлы на месте, байт в байт.
func sameGraph(t *testing.T, collDir, name string, files map[string]string) {
	t.Helper()
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(collDir, name, rel))
		if err != nil {
			t.Fatalf("после уплотнения пропал %s/%s: %v", name, rel, err)
		}
		if string(got) != want {
			t.Fatalf("%s/%s изменился: %q вместо %q", name, rel, got, want)
		}
	}
}

// noWorkDirs проверяет, что рабочих каталогов уплотнения не осталось.
func noWorkDirs(t *testing.T, base *Base) {
	t.Helper()
	entries, _ := os.ReadDir(base.CollectionsDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("остался рабочий каталог %s", e.Name())
		}
	}
}

// Именованный граф (`graph-lab`) — тоже граф: уплотнение без ключа его
// замечает и отказывает, а с ключом переносит в новый каталог нетронутым.
//
// До 07.10.2026 граф узнавался только по `graph/graph.meta`: именованный
// уплотнение не видело, шло без ключа и стирало его вместе с прежним каталогом.
func TestMergeKeepsNamedGraph(t *testing.T) {
	base, coll, _ := mergeFixture(t)
	files := fakeGraph(t, coll.Dir(), "graph-lab")
	// Чужой файл в корне коллекции — тоже не наш, и терять его нельзя.
	if err := os.WriteFile(filepath.Join(coll.Dir(), "README.local"), []byte("заметки"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !coll.HasGraph() {
		t.Fatal("именованный граф не распознан")
	}
	_, err := coll.Merge(context.Background(), MergeOpts{}, nil)
	if err == nil {
		t.Fatal("уплотнение при именованном графе прошло без --kb-merge-force")
	}
	if !strings.Contains(err.Error(), "graph-lab") {
		t.Errorf("в отказе не назван каталог графа: %v", err)
	}
	sameGraph(t, coll.Dir(), "graph-lab", files)

	if _, err := coll.Merge(context.Background(), MergeOpts{Force: true}, nil); err != nil {
		t.Fatalf("уплотнение с ключом: %v", err)
	}
	sameGraph(t, coll.Dir(), "graph-lab", files)
	if got, err := os.ReadFile(filepath.Join(coll.Dir(), "README.local")); err != nil || string(got) != "заметки" {
		t.Fatalf("чужой файл коллекции не пережил уплотнение: %q, %v", got, err)
	}
	noWorkDirs(t, base)
	if !found(t, coll, "goroutines") {
		t.Fatal("после уплотнения коллекция перестала искать")
	}
}

// С ключом --kb-merge-force рабочий граф не стирается: ключ разрешает
// уплотнение, а не удаление графа.
func TestMergeForceKeepsMainGraph(t *testing.T) {
	base, coll, _ := mergeFixture(t)
	files := fakeGraph(t, coll.Dir(), graphDirName)

	if _, err := coll.Merge(context.Background(), MergeOpts{Force: true}, nil); err != nil {
		t.Fatalf("уплотнение с ключом: %v", err)
	}
	sameGraph(t, coll.Dir(), graphDirName, files)
	noWorkDirs(t, base)
	// Копия замка, переехавшая с рабочим каталогом, снята вместе с уплотнением.
	if _, err := os.Stat(filepath.Join(coll.Dir(), lockMark)); !os.IsNotExist(err) {
		t.Fatalf("после уплотнения остался замок коллекции: %v", err)
	}

	// И новое открытие коллекции (а с ним recoverCompaction) граф не трогает.
	base2, err := OpenBase(base.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer base2.Close()
	if _, err := base2.Open("lib"); err != nil {
		t.Fatal(err)
	}
	sameGraph(t, coll.Dir(), graphDirName, files)
}

// Обрыв посреди подмены не теряет перенесённый граф: открытие коллекции
// возвращает его из рабочего каталога, а не стирает вместе с ним.
func TestRecoverCompactionReturnsCarriedGraph(t *testing.T) {
	for _, between := range []bool{false, true} {
		name := "после переноса"
		if between {
			name = "между переименованиями"
		}
		t.Run(name, func(t *testing.T) {
			base, coll, _ := mergeFixture(t)
			dir := coll.Dir()
			files := fakeGraph(t, dir, "graph-lab")
			before := coll.Stats().Chunks
			base.Close()

			// Готовый рабочий каталог уплотнения: файлы kb и перенесённый граф.
			tmp := base.tempDir("compact-lib")
			if err := os.MkdirAll(tmp, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tmp, "meta.json"), []byte(`{"name":"lib"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(dir, "graph-lab"), filepath.Join(tmp, "graph-lab")); err != nil {
				t.Fatal(err)
			}
			if between {
				// Прежний каталог уже отставлен, новый ещё не встал.
				if err := os.Rename(dir, base.tempDir("old-lib")); err != nil {
					t.Fatal(err)
				}
			}

			base2, err := OpenBase(base.Dir())
			if err != nil {
				t.Fatal(err)
			}
			defer base2.Close()
			c2, err := base2.Open("lib")
			if err != nil {
				t.Fatalf("коллекция не открылась после обрыва: %v", err)
			}
			sameGraph(t, dir, "graph-lab", files)
			noWorkDirs(t, base2)
			if got := c2.Stats().Chunks; got != before {
				t.Fatalf("после восстановления кусков %d, было %d", got, before)
			}
		})
	}
}

// Открытие коллекции посреди идущего уплотнения его рабочие каталоги не трогает.
//
// Открывают коллекцию все — служба, интерфейс, соседние команды, — и до
// 07.10.2026 каждое открытие сносило рабочий каталог уплотнения, не глядя
// на замок: `--kb-merge` падал на подмене, едва кто-то спросил базу знаний.
func TestOpenKeepsRunningCompaction(t *testing.T) {
	for _, stage := range []string{"замок в коллекции", "замок в рабочем каталоге", "между переименованиями"} {
		t.Run(stage, func(t *testing.T) {
			base, coll, _ := mergeFixture(t)
			dir := coll.Dir()
			tmp := base.tempDir("compact-lib")
			old := base.tempDir("old-lib")
			if err := os.MkdirAll(tmp, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tmp, "chunks.idx"), []byte("работа уплотнения"), 0o644); err != nil {
				t.Fatal(err)
			}
			// Замок живого процесса — нашего собственного: уплотнение идёт.
			switch stage {
			case "замок в коллекции":
				if err := placeMarker(filepath.Join(dir, lockMark)); err != nil {
					t.Fatal(err)
				}
			case "замок в рабочем каталоге":
				if err := placeMarker(filepath.Join(tmp, lockMark)); err != nil {
					t.Fatal(err)
				}
			case "между переименованиями":
				if err := placeMarker(filepath.Join(dir, lockMark)); err != nil {
					t.Fatal(err)
				}
				if err := placeMarker(filepath.Join(tmp, lockMark)); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(dir, old); err != nil {
					t.Fatal(err)
				}
			}

			base2, err := OpenBase(base.Dir())
			if err != nil {
				t.Fatal(err)
			}
			defer base2.Close()
			base2.Open("lib") // ошибка «коллекции нет» между переименованиями — честный ответ

			if got, err := os.ReadFile(filepath.Join(tmp, "chunks.idx")); err != nil || string(got) != "работа уплотнения" {
				t.Fatalf("рабочий каталог идущего уплотнения тронут: %q, %v", got, err)
			}
			if stage == "между переименованиями" {
				if _, err := os.Stat(old); err != nil {
					t.Fatalf("отставленный каталог идущего уплотнения тронут: %v", err)
				}
				if _, err := os.Stat(dir); err == nil {
					t.Fatal("открытие вернуло прежний каталог на место посреди чужой подмены")
				}
			}
		})
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
