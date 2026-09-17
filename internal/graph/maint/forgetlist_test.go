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
	keys, docs, err := readChunkList(file)
	if err != nil || len(keys) != 2 || len(docs) != 0 {
		t.Fatalf("прочитано %d номеров и %d книг, %v", len(keys), len(docs), err)
	}
	// Книга целиком.
	if err := os.WriteFile(file, []byte("// перед перечитыванием\n207#*  «Облачные архитектуры»\n12#37\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	keys, docs, err = readChunkList(file)
	if err != nil || len(keys) != 1 || !docs[207] {
		t.Fatalf("книга целиком: номеров %d, книг %v, %v", len(keys), docs, err)
	}
	if err := os.WriteFile(file, []byte("x#*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readChunkList(file); err == nil {
		t.Fatal("«x#*» принято")
	}
	if err := os.WriteFile(file, []byte("12#37\nне номер\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readChunkList(file); err == nil {
		t.Fatal("строка без номера куска обязана быть ошибкой, а не пропуском: опечатка в списке стоит недель карты")
	}
}
