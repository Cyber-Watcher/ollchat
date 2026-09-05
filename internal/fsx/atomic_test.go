package fsx

import (
	"os"
	"path/filepath"
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
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("права %o, ожидалось 644", info.Mode().Perm())
	}
}

func TestWriteFileAtomicMissingDir(t *testing.T) {
	err := WriteFileAtomic(filepath.Join(t.TempDir(), "no", "such", "dir", "f"), []byte("x"), 0o644)
	if err == nil {
		t.Fatal("запись в несуществующий каталог должна вернуть ошибку")
	}
}
