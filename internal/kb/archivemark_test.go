package kb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Признак архива с живым хозяином виден, с мёртвым — снимается сам.
func TestArchiveMarkLifecycle(t *testing.T) {
	dir := t.TempDir()
	if s := ArchiveInProgress(dir); s != "" {
		t.Fatalf("пустой каталог занят: %q", s)
	}
	release, err := MarkArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s := ArchiveInProgress(dir); s == "" {
		t.Fatal("поставленный признак не виден")
	}
	if _, err := MarkArchive(dir); err == nil {
		t.Error("второй архив разом не должен ставиться")
	}
	release()
	if s := ArchiveInProgress(dir); s != "" {
		t.Fatalf("после снятия признак виден: %q", s)
	}

	// Брошенный признак: номер процесса, которого нет.
	path := filepath.Join(dir, archiveMark)
	if err := os.WriteFile(path, []byte("999999999 2026-09-04T00:00:00Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := ArchiveInProgress(dir); s != "" {
		t.Fatalf("мёртвый хозяин считается живым: %q", s)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("брошенный признак не снят")
	}
}

// Ожидание кончается вместе с архивом, а по истечении срока говорит,
// кто держит и что делать.
func TestWaitArchive(t *testing.T) {
	dir := t.TempDir()
	release, err := MarkArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldPoll := archivePoll
	archivePoll = 20 * time.Millisecond
	defer func() { archivePoll = oldPoll }()

	err = WaitArchive(dir, 60*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "rm ") {
		t.Fatalf("по истечении срока ожидался отказ с подсказкой rm, получено: %v", err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		release()
	}()
	if err := WaitArchive(dir, time.Second); err != nil {
		t.Fatalf("после снятия признака ожидание должно кончиться: %v", err)
	}
}

// Замок индексации ждёт архив: под живым признаком он не ставится сразу,
// а по снятии — ставится.
func TestCollectionLockWaitsForArchive(t *testing.T) {
	c := &Collection{dir: t.TempDir(), name: "проба"}
	release, err := MarkArchive(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	oldWait, oldPoll := ArchiveWait, archivePoll
	ArchiveWait, archivePoll = 50*time.Millisecond, 10*time.Millisecond
	defer func() { ArchiveWait, archivePoll = oldWait, oldPoll }()

	if err := c.lock(); err == nil {
		t.Fatal("замок под архивом поставился, не дождавшись")
	}
	release()
	if err := c.lock(); err != nil {
		t.Fatalf("после архива замок должен ставиться: %v", err)
	}
	c.unlock()
	if s := CollectionBusy(c.dir); s != "" {
		t.Errorf("после снятия замка коллекция занята: %q", s)
	}
}
