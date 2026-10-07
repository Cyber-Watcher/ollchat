package find

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Подтверждения графа не выдают удалённую книгу.
//
// Граф помнит ссылки и на удалённые книги, а ChunkByRef отдавал их куски
// без признака удалённости: /search и инструменты показывали выдержку
// из книги, которую человек убрал, наравне с живыми (аудит 07.10.2026).
func TestFromGraphSkipsDeletedBooks(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	body := strings.Repeat("Подробный текст раздела о предмете книги, достаточно длинный для куска. ", 8)
	write("live.md", "# Живая книга\n\n"+body)
	gone := write("gone.md", "# Удалённая книга\n\n"+body)

	base, err := kb.OpenBase(filepath.Join(t.TempDir(), "kb"))
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	coll, err := base.Create("lib", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{dir}, kb.IndexOpts{Workers: 1}, nil); err != nil {
		t.Fatal(err)
	}
	ids := map[string]uint32{}
	for _, b := range coll.Books() {
		ids[filepath.Base(b.Path)] = b.ID
	}
	if ids["live.md"] == 0 || ids["gone.md"] == 0 {
		t.Fatalf("подготовка: книги не прочитались: %v", ids)
	}
	if err := coll.Forget(gone); err != nil {
		t.Fatal(err)
	}

	keys := []graph.ChunkKey{{Doc: ids["gone.md"], Ord: 0}, {Doc: ids["live.md"], Ord: 0}}
	got := fromGraph(coll, keys, Opts{Collection: "lib"}.norm())
	if len(got) != 1 {
		t.Fatalf("выдержек %d, ожидалась одна — из живой книги: %+v", len(got), got)
	}
	if !strings.HasPrefix(got[0].ID, fmt.Sprintf("lib/%d#", ids["live.md"])) {
		t.Fatalf("выдана не та книга: %s", got[0].ID)
	}
}
