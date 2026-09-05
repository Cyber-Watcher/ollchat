package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Признак работы виден в Busy с названием работы, снимается по release,
// а брошенный (мёртвый процесс) исчезает сам.
func TestMarkWorkAndBusy(t *testing.T) {
	_, collDir := fakeCollection(t, "books")
	// fakeCollection кладёт признак от заведомо неживого процесса.
	if b := Busy(collDir); b != "" {
		t.Fatalf("брошенный признак не должен считаться занятостью: %q", b)
	}
	if _, err := os.Stat(filepath.Join(collDir, DirName, "WORK-999999999")); err == nil {
		t.Errorf("брошенный признак не снят")
	}

	release, err := MarkWork(filepath.Join(collDir, DirName), "разметка тем")
	if err != nil {
		t.Fatal(err)
	}
	if b := Busy(collDir); !strings.Contains(b, "разметка тем") {
		t.Errorf("Busy не называет работу: %q", b)
	}
	release()
	if b := Busy(collDir); b != "" {
		t.Errorf("после снятия занятость осталась: %q", b)
	}
}

// Пишущая работа ждёт окончания архива, а не отказывает: ночная сборка
// по cron не должна срываться из-за секунд архива.
func TestMarkWorkWaitsForArchive(t *testing.T) {
	_, collDir := fakeCollection(t, "books")
	release, err := kb.MarkArchive(collDir)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		release()
	}()
	start := time.Now()
	rel, err := MarkWork(filepath.Join(collDir, DirName), "проба")
	if err != nil {
		t.Fatalf("MarkWork под коротким архивом: %v", err)
	}
	rel()
	if time.Since(start) < 100*time.Millisecond {
		t.Errorf("работа не дождалась архива")
	}
}

// Замок сборки тоже ждёт архив.
func TestLockWaitsForArchive(t *testing.T) {
	_, collDir := fakeCollection(t, "books")
	g, err := Open(collDir, 10, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	release, err := kb.MarkArchive(collDir)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		release()
	}()
	if err := g.Lock(); err != nil {
		t.Fatalf("Lock под коротким архивом: %v", err)
	}
	g.Unlock()
}

// Графы коллекции: рабочий и именованные, только с паспортом.
func TestGraphDirsListsNamedGraphs(t *testing.T) {
	_, collDir := fakeCollection(t, "books")
	if err := os.MkdirAll(filepath.Join(collDir, "graph-lab"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(collDir, "graphics"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirs := GraphDirs(collDir)
	if len(dirs) != 1 {
		t.Fatalf("без паспорта graph-lab не граф: %v", dirs)
	}
	if err := os.WriteFile(filepath.Join(collDir, "graph-lab", metaFile), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirs := GraphDirs(collDir); len(dirs) != 2 {
		t.Fatalf("ожидались graph и graph-lab: %v", dirs)
	}
	if !HasAnyGraph(collDir) {
		t.Error("HasAnyGraph")
	}
}
