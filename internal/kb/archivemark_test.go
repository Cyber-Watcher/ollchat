package kb

import (
	"fmt"
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

// Пустой или испорченный замок не роняет индексацию.
//
// Замок разбирался как strings.Fields(...)[0], и пустой LOCK — обрыв между
// созданием файла и записью в него — давал панику при каждой следующей
// индексации, пока файл не уберут руками. Давний испорченный замок
// снимается как брошенный, свежий пустой — занят: его вот-вот допишут.
func TestCollectionLockSurvivesGarbage(t *testing.T) {
	for _, body := range []string{"", "  \n", "мусор вместо номера"} {
		c := &Collection{dir: t.TempDir(), name: "проба"}
		path := filepath.Join(c.dir, lockMark)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := c.lock(); err == nil {
			t.Errorf("%q: свежий замок без номера процесса снят как брошенный", body)
			c.unlock()
		}
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		if err := c.lock(); err != nil {
			t.Fatalf("%q: давний испорченный замок не снялся: %v", body, err)
		}
		if got := markerPID(path); got != os.Getpid() {
			t.Fatalf("%q: в замке номер %d, а не свой", body, got)
		}
		c.unlock()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%q: свой замок не снят: %v", body, err)
		}
	}
}

// Из двух, пришедших за замком разом, его получает ровно один.
//
// Прежний замок ставился «прочитать, нет ли, — записать»: два процесса,
// прочитавшие «нет» одновременно, оба считали коллекцию своей и писали в одно
// хранилище. Теперь файл создаётся с O_EXCL.
func TestCollectionLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	for round := 0; round < 50; round++ {
		a := &Collection{dir: dir, name: "проба"}
		b := &Collection{dir: dir, name: "проба"}
		start := make(chan struct{})
		got := make(chan *Collection, 2)
		for _, c := range []*Collection{a, b} {
			go func(c *Collection) {
				<-start
				if c.lock() == nil {
					got <- c
				} else {
					got <- nil
				}
			}(c)
		}
		close(start)
		var owners []*Collection
		for i := 0; i < 2; i++ {
			if c := <-got; c != nil {
				owners = append(owners, c)
			}
		}
		if len(owners) != 1 {
			t.Fatalf("круг %d: замок получили %d претендента из двух", round, len(owners))
		}
		owners[0].unlock()
	}
}

// Снимается только свой замок: чужой, поставленный живым процессом, остаётся.
//
// Уплотнение подменяет каталог коллекции, и в новом каталоге замок мог успеть
// поставить другой процесс; безусловное удаление в конце уплотнения сняло бы
// его замок посреди его работы.
func TestCollectionUnlockKeepsForeignLock(t *testing.T) {
	c := &Collection{dir: t.TempDir(), name: "проба"}
	path := filepath.Join(c.dir, lockMark)
	// Родитель процесса тестов жив всё время теста, и это не мы.
	foreign := fmt.Sprintf("%d %s\n", os.Getppid(), time.Now().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	c.unlock()
	if data, err := os.ReadFile(path); err != nil || string(data) != foreign {
		t.Fatalf("чужой замок снят или испорчен: %q, %v", data, err)
	}
	if err := c.lock(); err == nil {
		t.Fatal("замок живого чужого процесса не остановил индексацию")
	}
}
