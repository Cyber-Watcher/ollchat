package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteFileAtomicCreatesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	if err := WriteFileAtomic(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("первая запись: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "first" {
		t.Fatalf("после первой записи: %q", got)
	}
	if err := WriteFileAtomic(path, []byte("second, longer"), 0o644); err != nil {
		t.Fatalf("вторая запись: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "second, longer" {
		t.Fatalf("после второй записи: %q", got)
	}

	// Временных файлов после себя не оставляет.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("в каталоге лишние файлы: %v", names)
	}
	// Сверх просимого прав не прибавляется; сколько отнимет umask — дело
	// umask (это проверяет TestWriteFileAtomicHonoursUmask).
	info, _ := os.Stat(path)
	if extra := info.Mode().Perm() &^ 0o644; extra != 0 {
		t.Fatalf("права %o: лишние биты %o", info.Mode().Perm(), extra)
	}
}

// Перезапись не трогает права файла: 0600 остаётся 0600, а не становится
// тем perm, с которым его переписали. До 07.10.2026 Chmod(perm) после
// переименования открывал закрытый файл всем.
func TestWriteFileAtomicKeepsExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("права после перезаписи %o, ожидалось 600", got)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Errorf("содержимое: %q", got)
	}
}

func TestWriteFileAtomicMissingDir(t *testing.T) {
	err := WriteFileAtomic(filepath.Join(t.TempDir(), "no", "such", "dir", "f"), []byte("x"), 0o644)
	if err == nil {
		t.Fatal("запись в несуществующий каталог должна вернуть ошибку")
	}
}

// Сброс каталога, которого файловая система не умеет, — не ошибка; сбой
// ввода-вывода — ошибка: файл мог не пережить отказ питания, и вызывающий
// должен это знать.
func TestDirSyncErr(t *testing.T) {
	for _, e := range []error{nil, syscall.EINVAL, errors.ErrUnsupported,
		&fs.PathError{Op: "sync", Path: "d", Err: syscall.EINVAL}} {
		if err := dirSyncErr("d", e); err != nil {
			t.Errorf("%v: считается ошибкой: %v", e, err)
		}
	}
	err := dirSyncErr("d", &fs.PathError{Op: "sync", Path: "d", Err: syscall.EIO})
	if err == nil || !errors.Is(err, syscall.EIO) {
		t.Errorf("EIO проглочен: %v", err)
	}
}
