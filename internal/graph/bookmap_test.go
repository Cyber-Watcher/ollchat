package graph

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// Сменившуюся нумерацию книг карта графа не принимает: заход сборки,
// запущенный раньше --graph-rebase-books, переписал бы её новыми номерами,
// и прежние номера, на которые ссылаются журналы, были бы забыты
// (аудит 07.10.2026, 4.5).
func TestRecordBooksRefusesMovedNumbering(t *testing.T) {
	g := newGraphWith(t, "a")
	dir := g.dir
	must(t, g.Close())
	if _, err := RecordBooks(dir, []KnownBook{{1, "h1", "a.pdf", 5}, {2, "h2", "b.pdf", 5}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, booksFile))
	if err != nil {
		t.Fatal(err)
	}

	swapped := []KnownBook{{1, "h2", "b.pdf", 5}, {2, "h1", "a.pdf", 5}}
	if _, err := RecordBooks(dir, swapped); !errors.Is(err, ErrBooksMoved) {
		t.Fatalf("карта при сменившейся нумерации: %v, ожидался ErrBooksMoved", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, booksFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("карта книг перезаписана при сменившейся нумерации")
	}
	// Перенос по сохранённой карте возможен, и после него карта снова пишется.
	if st, err := RebaseBooks(dir, swapped, false); err != nil || !st.Applied || st.Moved != 2 {
		t.Fatalf("перенос: %+v %v", st, err)
	}
	if _, err := RecordBooks(dir, swapped); err != nil {
		t.Fatalf("после переноса карта не пишется: %v", err)
	}
}

// Копия одного файла под двумя номерами — не переезд: обе книги на своих
// местах, и карта их записывает, а перенос ничего не трогает.
func TestRecordBooksAcceptsDuplicateFiles(t *testing.T) {
	g := newGraphWith(t, "a")
	dir := g.dir
	must(t, g.Close())
	books := []KnownBook{{3, "h", "a.pdf", 5}, {7, "h", "copy/a.pdf", 5}}
	if _, err := RecordBooks(dir, books); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordBooks(dir, books); err != nil {
		t.Fatalf("повторная запись карты с копией файла: %v", err)
	}
	if st, err := RebaseBooks(dir, books, true); err != nil || st.Moved != 0 || st.Collision != "" {
		t.Fatalf("копия файла принята за переезд: %+v %v", st, err)
	}
}

// swapFixture — граф, у которого книги 1 и 2 обменялись номерами: перенос —
// перестановка, и применённый дважды он вернул бы всё на прежние места.
func swapFixture(t *testing.T) (dir string, swapped []KnownBook) {
	t.Helper()
	g := newGraphWith(t, "горутина", "канал")
	dir = g.dir
	one, two := ChunkKey{Doc: 1, Ord: 1}, ChunkKey{Doc: 2, Ord: 1}
	must(t, g.Mentions().Add(1, one))
	must(t, g.Mentions().Add(2, two))
	must(t, g.Edges().Add(Edge{Src: 1, Dst: 2, Type: RelUses, Weight: 1, Evidence: one}))
	must(t, g.Progress().Mark(one, MarkDone))
	must(t, g.Progress().Mark(two, MarkEmpty))
	must(t, g.dropped.add(DropRec{Book: 1, Path: "a.pdf"}))
	if _, err := RecordBooks(dir, []KnownBook{{1, "h1", "a.pdf", 5}, {2, "h2", "b.pdf", 5}}); err != nil {
		t.Fatal(err)
	}
	must(t, g.Close())
	return dir, []KnownBook{{1, "h2", "b.pdf", 5}, {2, "h1", "a.pdf", 5}}
}

// Обрыв посреди подмены журналов не портит перестановку: повтор команды
// доводит перенос по плану, а уже подменённое не переносит второй раз.
// Пока перенос не доведён, сборка не идёт.
//
// До 07.10.2026 повтор после обрыва применял перестановку к уже
// перенесённому журналу ещё раз — и записи книги 1 возвращались к книге 1,
// а граф приписывал её кускам чужие понятия (аудит, 4.5).
func TestRebaseBooksResumesAfterCrash(t *testing.T) {
	dir, swapped := swapFixture(t)
	m, err := loadBookMap(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Обрыв: заготовки и план записаны, подменён только первый журнал.
	const stamp = "20261007-120000"
	plan, err := prepareRebase(dir, stamp, planRebase(m, swapped).Moves, m)
	if err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, plan.Files[0].Name)
	if _, err := swapIn(first, rebaseTmp(first, stamp), first+".bak-"+stamp); err != nil {
		t.Fatal(err)
	}

	g, err := Open(filepath.Dir(dir), 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	model := &model{answer: func(int) (string, error) { return goodAnswer, nil }}
	if _, err := Build(context.Background(), chunksFor(1, "/AI/c.pdf"), g, model, BuildOpts{Workers: 1}, nil); err == nil ||
		!strings.Contains(err.Error(), "перенос номеров книг прерван") {
		t.Fatalf("сборка при недоведённом переносе: %v", err)
	}
	must(t, g.Close())

	st, err := RebaseBooks(dir, swapped, false)
	if err != nil {
		t.Fatalf("повтор переноса: %v", err)
	}
	if !st.Applied || !st.Resumed {
		t.Fatalf("перенос не доведён по плану: %+v", st)
	}
	g2, err := Open(filepath.Dir(dir), 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if got := g2.Mentions().Of(1); len(got) != 1 || got[0].Doc != 2 {
		t.Errorf("упоминание книги 1 после переноса: %v, ожидалась книга 2", got)
	}
	if got := g2.Mentions().Of(2); len(got) != 1 || got[0].Doc != 1 {
		t.Errorf("упоминание книги 2 после переноса: %v, ожидалась книга 1", got)
	}
	if e := g2.Edges().Of(1); len(e) != 1 || e[0].Evidence.Doc != 2 {
		t.Errorf("подтверждение связи: %+v", e)
	}
	if mk, _ := g2.Progress().MarkOf(ChunkKey{Doc: 2, Ord: 1}); mk != MarkDone {
		t.Errorf("отметка разбора не переехала с книгой: %d", mk)
	}
	if !g2.Dropped().Dropped(2) || g2.Dropped().Dropped(1) {
		t.Error("отброшенная книга не переехала")
	}
	m2, err := loadBookMap(dir)
	if err != nil || m2.Books[2].Hash != "h1" || m2.Books[1].Hash != "h2" {
		t.Fatalf("карта после переноса: %+v %v", m2.Books, err)
	}
	if rebasePending(dir) {
		t.Error("план переноса остался после доведения")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".rebase-") {
			t.Errorf("заготовка осталась: %s", e.Name())
		}
	}
	if st, err := RebaseBooks(dir, swapped, false); err != nil || st.Moved != 0 {
		t.Fatalf("третий прогон что-то перенёс: %+v %v", st, err)
	}
}

// Журнал, переписанный после подготовки переноса, заготовкой не подменяется:
// она устарела, и подмена потеряла бы ту правку. Перенос останавливается
// с объяснением и с планом на месте.
func TestRebaseBooksRefusesStalePreparation(t *testing.T) {
	dir, swapped := swapFixture(t)
	m, err := loadBookMap(dir)
	if err != nil {
		t.Fatal(err)
	}
	const stamp = "20261007-120000"
	if _, err := prepareRebase(dir, stamp, planRebase(m, swapped).Moves, m); err != nil {
		t.Fatal(err)
	}
	appendBytes(t, filepath.Join(dir, edgesFile), make([]byte, edgeSize))
	if _, err := RebaseBooks(dir, swapped, false); err == nil || !strings.Contains(err.Error(), edgesFile) {
		t.Fatalf("перенос по устаревшей заготовке: %v", err)
	}
	if !rebasePending(dir) {
		t.Error("план убран, хотя перенос не доведён")
	}
}

// Заготовки без плана — следы подготовки, оборванной до его записи: перенос
// убирает их и идёт заново.
func TestRebaseBooksDropsLeftoversWithoutPlan(t *testing.T) {
	dir, swapped := swapFixture(t)
	stray := rebaseTmp(filepath.Join(dir, mentionsFile), "20261007-115959")
	must(t, os.WriteFile(stray, []byte("обрывок"), 0o644))
	st, err := RebaseBooks(dir, swapped, false)
	if err != nil || !st.Applied || st.Resumed {
		t.Fatalf("перенос: %+v %v", st, err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Error("ничья заготовка осталась")
	}
}
