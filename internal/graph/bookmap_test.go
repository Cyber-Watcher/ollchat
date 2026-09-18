package graph

import (
	"os"
	"path/filepath"
	"testing"
)

// Перенос графа на новую нумерацию книг: книга 3 стала книгой 9, книга 4 осталась
// собой, книга 5 из коллекции пропала — её номер не трогается. Переписываются
// упоминания, связи, отметки, синонимы формата 2, отброшенные книги, журнал
// связываний; копии прежних файлов остаются; сухой прогон ничего не пишет.
func TestRebaseBooksMovesEveryJournal(t *testing.T) {
	g := newGraphWith(t, "горутина", "канал")
	dir := g.dir
	if err := g.Mentions().Add(1, ChunkKey{Doc: 3, Ord: 7}); err != nil {
		t.Fatal(err)
	}
	if err := g.Mentions().Add(2, ChunkKey{Doc: 4, Ord: 1}); err != nil {
		t.Fatal(err)
	}
	if err := g.Edges().Add(Edge{Src: 1, Dst: 2, Type: RelUses, Weight: 1, Evidence: ChunkKey{Doc: 3, Ord: 7}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Progress().Mark(ChunkKey{Doc: 3, Ord: 7}, 1); err != nil {
		t.Fatal(err)
	}
	if err := g.Progress().Mark(ChunkKey{Doc: 5, Ord: 2}, 1); err != nil {
		t.Fatal(err)
	}
	if err := g.Links().Add(LinkRec{Norm: "канал", Name: "канал", From: 2, Cand: 1, Verdict: LinkDoubt, Chunk: ChunkKey{Doc: 3, Ord: 7}}); err != nil {
		t.Fatal(err)
	}
	if err := g.dropped.add(DropRec{Book: 3, Path: "old.pdf"}); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordBooks(dir, []KnownBook{{3, "hashA", "a.pdf", 10}, {4, "hashB", "b.pdf", 10}, {5, "hashC", "c.pdf", 10}}); err != nil {
		t.Fatal(err)
	}
	g.Close()

	now := []KnownBook{{9, "hashA", "a.pdf", 10}, {4, "hashB", "b.pdf", 10}}
	st, err := RebaseBooks(dir, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.Applied || st.Moved != 1 || st.Same != 1 || st.Unknown != 1 || st.Moves[3] != 9 {
		t.Fatalf("сухой прогон: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(dir, mentionsFile+".bak-")); err == nil {
		t.Fatal("сухой прогон оставил копию")
	}

	st, err = RebaseBooks(dir, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Applied || len(st.Backups) < 5 {
		t.Fatalf("перенос: %+v", st)
	}
	t.Logf("переписано: %v, копии: %v", st.Files, st.Backups)
	g2, err := Open(filepath.Dir(dir), 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if m := g2.Mentions().Of(1); len(m) != 1 || m[0] != (ChunkKey{Doc: 9, Ord: 7}) {
		t.Fatalf("упоминание не перенесено: %v", m)
	}
	if m := g2.Mentions().Of(2); len(m) != 1 || m[0].Doc != 4 {
		t.Fatalf("упоминание книги, оставшейся на месте, испорчено: %v", m)
	}
	if e := g2.Edges().Of(1); len(e) != 1 || e[0].Evidence.Doc != 9 {
		t.Fatalf("подтверждение связи не перенесено: %+v", e)
	}
	if _, ok := g2.Progress().MarkOf(ChunkKey{Doc: 9, Ord: 7}); !ok {
		t.Fatal("отметка разбора не перенесена")
	}
	if _, ok := g2.Progress().MarkOf(ChunkKey{Doc: 5, Ord: 2}); !ok {
		t.Fatal("отметка пропавшей книги должна остаться под прежним номером")
	}
	if !g2.Dropped().Dropped(9) || g2.Dropped().Dropped(3) {
		t.Fatal("отброшенная книга не перенесена")
	}
	q := g2.Links().Queue()
	if len(q) != 1 || q[0].Chunk.Doc != 9 {
		t.Fatalf("журнал связываний не перенесён: %+v", q)
	}
	m2, err := loadBookMap(dir)
	if err != nil || m2.Books[9].Hash != "hashA" || m2.Books[5].Hash != "hashC" {
		t.Fatalf("карта после переноса: %+v %v", m2.Books, err)
	}
	if _, stale := m2.Books[3]; stale {
		t.Fatal("прежний номер остался в карте")
	}
	// Повторный перенос ничего не меняет.
	if st, err := RebaseBooks(dir, now, true); err != nil || st.Moved != 0 {
		t.Fatalf("после переноса перенос должен быть пуст: %+v %v", st, err)
	}
}

// Перенос отказывает, когда два прежних номера ведут в один новый или новый
// номер занят книгой, которая остаётся на месте: переписать так значит
// смешать две книги.
func TestRebaseBooksRefusesCollisions(t *testing.T) {
	g := newGraphWith(t, "a")
	dir := g.dir
	g.Close()
	if _, err := RecordBooks(dir, []KnownBook{{1, "h1", "", 5}, {2, "h2", "", 5}}); err != nil {
		t.Fatal(err)
	}
	st, err := RebaseBooks(dir, []KnownBook{{2, "h1", "", 5}, {5, "hX", "", 5}}, true)
	if err != nil || st.Collision == "" {
		t.Fatalf("занятый номер должен дать отказ: %+v %v", st, err)
	}
	st, err = RebaseBooks(dir, []KnownBook{{7, "h1", "", 5}, {7, "h2", "", 5}}, true)
	if err != nil || st.Collision == "" {
		t.Fatalf("два прежних номера в один новый должны дать отказ: %+v %v", st, err)
	}
	// Обмен номерами двух книг — допустим: каждая запись переписывается один раз.
	st, err = RebaseBooks(dir, []KnownBook{{2, "h1", "", 5}, {1, "h2", "", 5}}, true)
	if err != nil || st.Collision != "" || st.Moved != 2 {
		t.Fatalf("обмен номерами: %+v %v", st, err)
	}
	// Тот же файл под новым номером, но с другой нарезкой — книга перечитана,
	// а не переехала: переносить её записи нельзя.
	st, err = RebaseBooks(dir, []KnownBook{{9, "h1", "", 12}, {2, "h2", "", 5}}, true)
	if err != nil || st.Moved != 0 || st.Reread != 1 || st.Same != 1 {
		t.Fatalf("перечитанная книга: %+v %v", st, err)
	}
}
