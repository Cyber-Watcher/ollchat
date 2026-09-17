package maint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadChunkList(t *testing.T) {
	file := filepath.Join(t.TempDir(), "list.txt")
	body := "// перепись ложных раскрытий RE\n\n" +
		"12#37\tRelation extraction ← re library\n" +
		"  12#38   вторая причина\n" +
		"12#37 повтор не удваивается\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	keys, err := readChunkList(file)
	if err != nil || len(keys) != 2 {
		t.Fatalf("прочитано %d номеров, %v", len(keys), err)
	}
	if err := os.WriteFile(file, []byte("12#37\nне номер\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readChunkList(file); err == nil {
		t.Fatal("строка без номера куска обязана быть ошибкой, а не пропуском: опечатка в списке стоит недель карты")
	}
}
