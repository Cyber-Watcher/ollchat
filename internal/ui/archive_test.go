package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// baseWithGraph — база с одной коллекцией, у которой есть паспорт графа,
// и одной без графа.
func baseWithGraph(t *testing.T) *kb.Base {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"books", "notes"} {
		dir := filepath.Join(root, "collections", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"name":"`+name+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gdir := filepath.Join(root, "collections", "books", graph.DirName)
	if err := os.MkdirAll(gdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gdir, "graph.meta"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	base, err := kb.OpenBase(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	return base
}

// Пора — когда у коллекции с графом нет свежего архива и с ней никто
// не работает; коллекция без графа в расписание не попадает.
func TestDueCollection(t *testing.T) {
	base := baseWithGraph(t)
	dir := t.TempDir()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.Local)
	every := 24 * time.Hour

	if got := dueCollection(base, dir, every, now); got != "books" {
		t.Fatalf("без архивов пора books, получено %q", got)
	}

	// Свежий архив — не пора; старый — пора.
	fresh := filepath.Join(dir, graph.ArchiveName("books", now.Add(-time.Hour), ""))
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := dueCollection(base, dir, every, now); got != "" {
		t.Errorf("при архиве часовой давности не пора, получено %q", got)
	}
	os.Remove(fresh)
	old := filepath.Join(dir, graph.ArchiveName("books", now.Add(-25*time.Hour), ""))
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := dueCollection(base, dir, every, now); got != "books" {
		t.Errorf("при архиве суточной давности пора, получено %q", got)
	}

	// Снимок «перед восстановлением» расписания не сдвигает.
	tagged := filepath.Join(dir, graph.ArchiveName("books", now.Add(-time.Minute), "before-restore"))
	if err := os.WriteFile(tagged, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := dueCollection(base, dir, every, now); got != "books" {
		t.Errorf("снимок с пометкой не считается плановым, получено %q", got)
	}

	// Идёт работа с графом — не пора.
	release, err := graph.MarkWork(base.CollectionDir("books")+"/"+graph.DirName, "проба")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if got := dueCollection(base, dir, every, now); got != "" {
		t.Errorf("под работой не пора, получено %q", got)
	}
}

// Значок в строке состояния есть только пока архив идёт.
func TestArchiveStatusSegment(t *testing.T) {
	m := &Model{}
	if s := m.archiveStatus(time.Now()); s != "" {
		t.Fatalf("без архива сегмент должен быть пуст: %q", s)
	}
	start := time.Now()
	m.archive = &archiveJob{coll: "books", started: start}
	got := m.archiveStatus(start.Add(12 * time.Second))
	if got != archiveIcon+" архив books 12s" {
		t.Errorf("сегмент: %q", got)
	}
}
