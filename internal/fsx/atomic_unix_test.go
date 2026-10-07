//go:build unix

package fsx

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Новый файл получает perm за вычетом umask, как у os.WriteFile. До 07.10.2026
// Chmod(perm) после записи umask не замечал: при umask 027 файл выходил
// 0666, открытым на запись всем.
func TestWriteFileAtomicHonoursUmask(t *testing.T) {
	old := syscall.Umask(0o027)
	t.Cleanup(func() { syscall.Umask(old) })

	path := filepath.Join(t.TempDir(), "new.json")
	if err := WriteFileAtomic(path, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("права нового файла %o, ожидалось 640 (666 за вычетом umask 027)", got)
	}
}
