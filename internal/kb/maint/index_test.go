package maint

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Коллекция, которая есть, но не открывается, сообщает свою ошибку,
// а не отказ «коллекция уже есть».
//
// --kb-index на неоткрывшейся коллекции пробовал завести её заново, и человек
// видел отказ Create вместо настоящей причины — испорченного паспорта.
func TestIndexShowsOpenErrorOfExistingCollection(t *testing.T) {
	dir := t.TempDir()
	books := filepath.Join(dir, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	base, err := kb.OpenBase(filepath.Join(dir, "kb"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Create("lib", ""); err != nil {
		t.Fatal(err)
	}
	base.Close()
	meta := filepath.Join(base.CollectionDir("lib"), "meta.json")
	if err := os.WriteFile(meta, []byte("{испорчено"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.KB.Dir = base.Dir()
	cfg.KB.Roots = []string{books}
	err = Index(io.Discard, cfg, "lib", []string{books}, false, false, false)
	if err == nil {
		t.Fatal("испорченная коллекция проиндексировалась")
	}
	if strings.Contains(err.Error(), "уже есть") {
		t.Fatalf("настоящая причина подменена отказом Create: %v", err)
	}
	// Паспорт не тронут: чинить его — дело человека, а не попытки «завести заново».
	if got, _ := os.ReadFile(meta); string(got) != "{испорчено" {
		t.Fatalf("паспорт коллекции переписан: %q", got)
	}
}
