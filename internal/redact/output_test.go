package redact

import (
	"os"
	"path/filepath"
	"testing"
)

// Итоги пишутся с правами только владельцу: в распознанных копиях и разборе
// -report лежат все персональные данные документа. Прежде файл открывался
// всем на чтение (0644), в том числе поверх прежнего файла с правами 0600.
func TestWriteFileOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "новый каталог")
	path := filepath.Join(dir, "скан.ocr.md")
	if err := WriteFile(path, []byte("все данные")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("все данные ещё раз")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("права итога %o, а должны быть 600", mode)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("права созданного каталога: %v, %v", info.Mode().Perm(), err)
	}
}
