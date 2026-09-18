package maint

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
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

// Книга целиком («N#*») забывается и после удаления книги с диска.
//
// 18.09.2026: три EPUB-копии убраны из библиотеки, `--kb-sync` пометил их
// удалёнными, а чистка по списку «327#*» отказала: «в коллекции нет кусков» —
// книга разворачивалась по живым кускам коллекции, которых у удалённой книги
// уже нет. Между тем в графе её след остался. Теперь книга сверяется
// с реестром коллекции, а куски отбираются по номеру книги в самих журналах.
func TestForgetListDropsDeletedBook(t *testing.T) {
	root := t.TempDir()
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	// Тексты разные: одинаковые файлы коллекция считает одной книгой.
	page := strings.Repeat("plain sentence of book text about the subject. ", 40)
	keep := filepath.Join(books, "keep.txt")
	gone := filepath.Join(books, "gone.txt")
	if err := os.WriteFile(keep, []byte("kubernetes deployment. "+page), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gone, []byte("goroutines and channels. "+page), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.KB.Dir = filepath.Join(root, "kb")
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		t.Fatal(err)
	}
	coll, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := coll.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, kb.IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	var keepID, goneID uint32
	for _, b := range coll.Books() {
		switch filepath.Base(b.Path) {
		case "keep.txt":
			keepID = b.ID
		case "gone.txt":
			goneID = b.ID
		}
	}
	if keepID == 0 || goneID == 0 {
		t.Fatalf("книги не получили номеров: %+v", coll.Books())
	}

	// Граф с упоминаниями из обеих книг.
	g, err := graph.Create(coll.Dir(), coll.Name(), coll.Stats().Chunks, cfg.Graph.Rules())
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := g.Entities().Add("CoreDNS", graph.TypeTech)
	kk := graph.ChunkKey{Doc: keepID, Ord: 0}
	gk := graph.ChunkKey{Doc: goneID, Ord: 0}
	for _, k := range []graph.ChunkKey{kk, gk} {
		if err := g.Mentions().Add(a, k); err != nil {
			t.Fatal(err)
		}
		if err := g.Progress().Mark(k, graph.MarkDone); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	// Книга убрана с диска и помечена удалённой.
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Sync(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(coll.DeletedBooks()) != 1 {
		t.Fatalf("удалённых книг %d, ожидалась 1", len(coll.DeletedBooks()))
	}
	base.Close()

	list := filepath.Join(root, "forget.txt")
	if err := os.WriteFile(list, []byte("// EPUB-копия\n"+strings.TrimSuffix(gk.String(), "#0")+"#*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ForgetList(io.Discard, cfg, "proba", list, true, true); err != nil {
		t.Fatalf("сухой прогон по удалённой книге: %v", err)
	}
	if err := ForgetList(io.Discard, cfg, "proba", list, true, false); err != nil {
		t.Fatalf("чистка по удалённой книге: %v", err)
	}
	// Книги, которой нет и не было, список не принимает.
	if err := os.WriteFile(list, []byte("999#*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ForgetList(io.Discard, cfg, "proba", list, true, true); err == nil {
		t.Fatal("книга №999 принята, хотя её нет ни в реестре, ни среди удалённых")
	}

	g2, err := graph.Open(coll.Dir(), 0, cfg.Graph.Rules())
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if got := g2.Mentions().Of(a); len(got) != 1 || got[0] != kk {
		t.Fatalf("после чистки упоминания %v, ожидалось только %v", got, kk)
	}
	if m, ok := g2.Progress().MarkOf(gk); !ok || m != graph.MarkSkipped {
		t.Fatalf("отметка удалённого куска %d/%v, ожидалась «пропущен»", m, ok)
	}
}
