package graph

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCollection — каталог коллекции с графом во временном каталоге
// коллекций. Возвращает каталог коллекций и каталог самой коллекции.
func fakeCollection(t *testing.T, name string) (collsDir, collDir string) {
	t.Helper()
	collsDir = filepath.Join(t.TempDir(), "collections")
	collDir = filepath.Join(collsDir, name)
	if err := os.MkdirAll(collDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		p := filepath.Join(collDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("meta.json", `{"name":"`+name+`"}`)
	write("chunks.dat", strings.Repeat("кусок текста ", 200))
	write("docs.jsonl", `{"id":1,"path":"/книги/a.pdf"}`+"\n")
	g, err := Create(collDir, name, 10, Rules{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	// То, что в архив идти не должно.
	write("graph/entities.jsonl.bak-20260902-224230", "старый реестр")
	write("graph/WORK-999999999", "pid 999999999, начато 2026-09-04T00:00:00Z, работа: проба")
	return collsDir, collDir
}

// archiveEntries — имена записей архива.
func archiveEntries(t *testing.T, path string) []string {
	t.Helper()
	var names []string
	err := walkArchive(path, func(hdr *tar.Header, _ io.Reader) error {
		names = append(names, hdr.Name)
		return nil
	})
	if err != nil {
		t.Fatalf("walkArchive: %v", err)
	}
	return names
}

// Архив: имя по образцу, внутри коллекция целиком, без резервных копий,
// признаков и замков; готовый файл без хвоста .part.
func TestArchiveWritesCollectionWithoutMarkers(t *testing.T) {
	_, collDir := fakeCollection(t, "books")
	dir := filepath.Join(t.TempDir(), "kb_archives")

	res, err := Archive(collDir, ArchiveOpts{Dir: dir, Keep: 7})
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	coll, _, tag, ok := parseArchiveName(filepath.Base(res.Path))
	if !ok || coll != "books" || tag != "" {
		t.Fatalf("имя архива не по образцу: %s", res.Path)
	}
	if res.Wrote <= 0 || res.Files == 0 {
		t.Fatalf("пустой итог: %+v", res)
	}
	if _, err := os.Stat(res.Path + ".part"); err == nil {
		t.Errorf("хвост .part остался рядом с готовым архивом")
	}
	names := strings.Join(archiveEntries(t, res.Path), "\n")
	for _, want := range []string{"books/meta.json", "books/chunks.dat", "books/graph/graph.meta"} {
		if !strings.Contains(names, want) {
			t.Errorf("в архиве нет %s:\n%s", want, names)
		}
	}
	for _, bad := range []string{".bak-", "WORK-", "LOCK", "ARCHIVE"} {
		if strings.Contains(names, bad) {
			t.Errorf("в архив попало лишнее %q:\n%s", bad, names)
		}
	}
	if _, err := os.Stat(filepath.Join(collDir, "ARCHIVE")); err == nil {
		t.Errorf("признак архива не снят после окончания")
	}
	last, ok := LastArchive(dir, "books")
	if !ok || last.Path != res.Path {
		t.Errorf("LastArchive не видит только что снятый архив: %+v", last)
	}
}

// Пока идёт сборка (живой замок) или помеченная работа, архив не делается,
// а отказ называет, кто мешает.
func TestArchiveRefusesWhileBusy(t *testing.T) {
	_, collDir := fakeCollection(t, "books")
	dir := t.TempDir()

	g, err := Open(collDir, 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if err := g.Lock(); err != nil {
		t.Fatal(err)
	}
	_, err = Archive(collDir, ArchiveOpts{Dir: dir})
	if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "сборка графа") {
		t.Fatalf("под замком сборки ожидался ErrBusy со словом «сборка графа», получено: %v", err)
	}
	g.Unlock()

	release, err := MarkWork(g.Dir(), "векторы понятий")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Archive(collDir, ArchiveOpts{Dir: dir})
	if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "векторы понятий") {
		t.Fatalf("под признаком работы ожидался ErrBusy с названием работы, получено: %v", err)
	}
	release()

	if _, err := Archive(collDir, ArchiveOpts{Dir: dir}); err != nil {
		t.Fatalf("после снятия признака архив должен идти: %v", err)
	}
}

// Ротация оставляет keep новейших плановых архивов; снимок перед
// восстановлением не считается и не удаляется.
func TestArchiveRotationKeepsNewest(t *testing.T) {
	_, collDir := fakeCollection(t, "books")
	dir := t.TempDir()

	base := time.Date(2026, 9, 4, 11, 0, 0, 0, time.Local)
	step := 0
	archiveNow = func() time.Time { step++; return base.Add(time.Duration(step) * time.Minute) }
	defer func() { archiveNow = time.Now }()

	if _, err := Archive(collDir, ArchiveOpts{Dir: dir, Tag: "before-restore"}); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for i := 0; i < 4; i++ {
		res, err := Archive(collDir, ArchiveOpts{Dir: dir, Keep: 2})
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, res.Path)
		if i == 3 && len(res.Removed) != 1 {
			t.Errorf("на четвёртом архиве при keep=2 должен уйти один: %v", res.Removed)
		}
	}
	list, err := Archives(dir, "books")
	if err != nil {
		t.Fatal(err)
	}
	var planned, tagged int
	for _, a := range list {
		if a.Tag == "" {
			planned++
		} else {
			tagged++
		}
	}
	if planned != 2 || tagged != 1 {
		t.Fatalf("после ротации ожидалось 2 плановых и 1 с пометкой, получено %d и %d: %+v", planned, tagged, list)
	}
	if list[0].Path != paths[3] {
		t.Errorf("новейший архив должен идти первым: %s", list[0].Path)
	}
	for _, p := range paths[:2] {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("старый архив не удалён: %s", p)
		}
	}
}

// Восстановление: в пустой каталог коллекций и поверх существующей коллекции.
// Поверх — прежняя сперва уходит в архив с пометкой, потом подменяется.
func TestRestoreRoundTripAndReplace(t *testing.T) {
	collsDir, collDir := fakeCollection(t, "books")
	dir := t.TempDir()
	res, err := Archive(collDir, ArchiveOpts{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	// В пустое место.
	other := filepath.Join(t.TempDir(), "collections")
	rr, err := Restore(res.Path, other, ArchiveOpts{Dir: dir})
	if err != nil {
		t.Fatalf("Restore в пустой каталог: %v", err)
	}
	if rr.Backup != "" {
		t.Errorf("коллекции не было, а снимок «до» сделан: %s", rr.Backup)
	}
	if _, err := os.Stat(filepath.Join(other, "books", "graph", metaFile)); err != nil {
		t.Fatalf("паспорт графа не восстановлен: %v", err)
	}
	if _, ok := rr.Graphs[DirName]; !ok {
		t.Errorf("в итоге нет паспорта рабочего графа: %+v", rr.Graphs)
	}
	if _, err := Open(filepath.Join(other, "books"), 10, Rules{}); err != nil {
		t.Fatalf("восстановленный граф не открывается: %v", err)
	}

	// Поверх: портим текущую коллекцию и возвращаем из архива.
	if err := os.WriteFile(filepath.Join(collDir, "chunks.dat"), []byte("испорчено"), 0o644); err != nil {
		t.Fatal(err)
	}
	rr, err = Restore(res.Path, collsDir, ArchiveOpts{Dir: dir})
	if err != nil {
		t.Fatalf("Restore поверх: %v", err)
	}
	if rr.Backup == "" {
		t.Fatalf("прежняя коллекция должна была уйти в архив")
	}
	if _, _, tag, ok := parseArchiveName(filepath.Base(rr.Backup)); !ok || tag != "before-restore" {
		t.Errorf("снимок «до» назван не так: %s", rr.Backup)
	}
	b, _ := os.ReadFile(filepath.Join(collDir, "chunks.dat"))
	if string(b) == "испорчено" {
		t.Errorf("содержимое не подменено")
	}
	for _, leftover := range []string{".books.restore", ".books.replaced"} {
		if _, err := os.Stat(filepath.Join(collsDir, leftover)); err == nil {
			t.Errorf("рабочий каталог %s остался после восстановления", leftover)
		}
	}
	// Восстановление поверх занятой коллекции отклоняется.
	g, err := Open(collDir, 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if err := g.Lock(); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(res.Path, collsDir, ArchiveOpts{Dir: dir}); !errors.Is(err, ErrBusy) {
		t.Fatalf("поверх идущей сборки ожидался ErrBusy, получено: %v", err)
	}
}

// Путь наружу из каталога распаковки отклоняется до того, как что-то записано.
func TestRestoreRejectsPathOutside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "books-20260904-110000.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"books/meta.json", "books/../../evil"} {
		body := []byte("x")
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(body)
	}
	tw.Close()
	gz.Close()
	f.Close()

	colls := filepath.Join(t.TempDir(), "collections")
	_, err = Restore(path, colls, ArchiveOpts{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "недопустимый путь") {
		t.Fatalf("ожидался отказ по пути наружу, получено: %v", err)
	}
	if _, err := os.Stat(filepath.Join(colls, "books")); err == nil {
		t.Errorf("коллекция создана из отклонённого архива")
	}
}

// Оглавление архива читается без распаковки: имя коллекции и паспорт графа.
func TestPeekArchiveReadsGraphMeta(t *testing.T) {
	_, collDir := fakeCollection(t, "books")
	res, err := Archive(collDir, ArchiveOpts{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	p, err := PeekArchive(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Collection != "books" {
		t.Errorf("коллекция %q", p.Collection)
	}
	m, ok := p.Graphs[DirName]
	if !ok || m.Collection != "books" {
		t.Errorf("паспорт графа не прочитан: %+v", p.Graphs)
	}
}

// Имя архива разбирается туда и обратно; чужие файлы в каталоге не мешают.
func TestArchiveNameRoundTrip(t *testing.T) {
	ts := time.Date(2026, 9, 4, 11, 13, 5, 0, time.Local)
	name := ArchiveName("books", ts, "before-restore")
	coll, got, tag, ok := parseArchiveName(name)
	if !ok || coll != "books" || !got.Equal(ts) || tag != "before-restore" {
		t.Fatalf("%s → %q %v %q %v", name, coll, got, tag, ok)
	}
	for _, bad := range []string{"books.tar.gz", "books-2026.tar.gz", "заметки.txt", "books-20260904-111305.tar.gz.part"} {
		if _, _, _, ok := parseArchiveName(bad); ok {
			t.Errorf("%q принято за архив", bad)
		}
	}
}
