package maint

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Приём на другой машине называет «не нашлось» только живые книги.
//
// Записи, помеченные удалёнными, остаются в реестре с путями, которых давно нет
// (переименованные файлы плана), и раньше приём печатал их как потерянные: владелец
// принял `project_plan.md` и `stage01.md` за пропавшие, хотя файлы лежали на месте.
// Здесь — через тот же вызов, что и у ключа `--kb-rebase`, а не соседний.
func TestRebasePrintsMissingOnlyForLiveBooks(t *testing.T) {
	dir := t.TempDir()
	base, err := kb.OpenBase(filepath.Join(dir, "kb"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Create("proba", ""); err != nil {
		t.Fatal(err)
	}
	base.Close()

	// Реестр пишется напрямую: перенос правит реестр, а не индексирует.
	oldRoot := filepath.Join(dir, "old")
	newRoot := filepath.Join(dir, "new")
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	var reg bytes.Buffer
	for i, n := range []string{"старое-имя.md", "живая-пропала.md", "на-месте.md"} {
		line, err := json.Marshal(kb.BookRec{ID: uint32(i + 1), Path: filepath.Join(oldRoot, n), Kind: kb.BookOK})
		if err != nil {
			t.Fatal(err)
		}
		reg.Write(append(line, '\n'))
	}
	collDir := base.CollectionDir("proba")
	if err := os.WriteFile(filepath.Join(collDir, "docs.jsonl"), reg.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// Книга 1 удалена; её файла по новому пути нет и не должно быть.
	if err := os.WriteFile(filepath.Join(collDir, "deleted.ids"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Книга 3 лежит на новом месте; книга 2 живая, но файл пропал.
	if err := os.WriteFile(filepath.Join(newRoot, "на-месте.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.KB.Dir = base.Dir()
	var out bytes.Buffer
	if err := Rebase(&out, cfg, "proba", oldRoot, newRoot, true); err != nil {
		t.Fatalf("перенос не прошёл: %v", err)
	}
	got := out.String()

	if !strings.Contains(got, "книг с новым путём 3") {
		t.Errorf("не сказано, что переписаны все три пути:\n%s", got)
	}
	if !strings.Contains(got, "живая-пропала.md") {
		t.Errorf("живая книга без файла не названа:\n%s", got)
	}
	if strings.Contains(got, "старое-имя.md") {
		t.Errorf("удалённая книга названа пропавшей:\n%s", got)
	}
	if !strings.Contains(got, "помечены удалёнными и без файла по новому пути: 1") {
		t.Errorf("число удалённых без файла не напечатано:\n%s", got)
	}
}
