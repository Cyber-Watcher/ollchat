package kb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reanalyzeFixture собирает коллекцию из одного текстового файла, в котором
// искомое слово разорвано переносом строки.
func reanalyzeFixture(t *testing.T) (*Base, *Collection) {
	t.Helper()
	root := t.TempDir()
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	text := "# Глава\n\n" + strings.Repeat("Вводные слова без отношения к делу.\n", 5) +
		"Здесь описаны популярные алго-\nритмы сортировки и поиска.\n"
	if err := os.WriteFile(filepath.Join(books, "book.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	base, err := OpenBase(filepath.Join(root, "kb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll, err := base.Create("t", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{books}, IndexOpts{Workers: 1}, nil); err != nil {
		t.Fatal(err)
	}
	return base, coll
}

// downgrade подменяет сегменты коллекции «старым» индексом: таким, каким его
// собрали бы правила, не умеющие склеивать переносы, — слово лежит обрубками.
func downgrade(t *testing.T, coll *Collection) {
	t.Helper()
	coll.mu.Lock()
	coll.meta.Analyzer = "ru-en-v1"
	coll.mu.Unlock()
	if err := coll.saveMeta(); err != nil {
		t.Fatal(err)
	}
}

func TestReanalyzeKeepsChunksAndUpdatesPassport(t *testing.T) {
	_, coll := reanalyzeFixture(t)
	downgrade(t, coll)
	if !coll.Stats().Stale {
		t.Fatal("коллекция со старыми правилами должна считаться устаревшей")
	}
	before := coll.Stats()
	chunksBefore, err := os.ReadFile(filepath.Join(coll.Dir(), "chunks.dat"))
	if err != nil {
		t.Fatal(err)
	}

	res, err := coll.Reanalyze(context.Background(), nil)
	if err != nil {
		t.Fatalf("пересборка: %v", err)
	}
	after := coll.Stats()
	if after.Stale || after.Analyzer != AnalyzerVersion {
		t.Fatalf("паспорт не обновлён: %+v", after)
	}
	if after.Chunks != before.Chunks || res.Chunks != before.Chunks {
		t.Fatalf("число кусков изменилось: %d → %d", before.Chunks, after.Chunks)
	}
	// Главное обещание: хранилище кусков не тронуто ни на байт — на номерах
	// кусков держатся векторы и граф.
	chunksAfter, _ := os.ReadFile(filepath.Join(coll.Dir(), "chunks.dat"))
	if string(chunksBefore) != string(chunksAfter) {
		t.Fatal("хранилище кусков изменилось")
	}
	// Мусора после подмены не остаётся.
	entries, _ := os.ReadDir(coll.Dir())
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, newSegPrefix) || strings.HasPrefix(n, deadSegPrefix) || n == reanalyzeMark || n == lockMark {
			t.Errorf("после пересборки остался %s", n)
		}
	}
	// И поиск находит слово, разорванное переносом.
	hits, err := coll.Search("алгоритмы", DefaultSearchOpts())
	if err != nil || len(hits) == 0 {
		t.Fatalf("слово через перенос не найдено: %v %v", hits, err)
	}
}

// Подмена, прерванная на середине, доводится до конца при следующем открытии.
func TestReanalyzeRecoversInterruptedSwap(t *testing.T) {
	base, coll := reanalyzeFixture(t)
	dir := coll.Dir()
	old, err := segmentDirs(dir)
	if err != nil || len(old) == 0 {
		t.Fatalf("нет сегментов: %v", err)
	}
	// Состояние «план записан, прежний сегмент уже убран, новый ещё не встал».
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildSegment(filepath.Join(dir, newSegPrefix+"00009"), store, 0, store.Count(), nil); err != nil {
		t.Fatal(err)
	}
	store.Close()
	plan := reanalyzePlan{Old: []string{filepath.Base(old[0])}, New: []string{newSegPrefix + "00009"}, Analyzer: AnalyzerVersion, NextSeg: 10}
	if err := writeJSON(filepath.Join(dir, reanalyzeMark), plan); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(old[0], filepath.Join(dir, deadSegPrefix+filepath.Base(old[0]))); err != nil {
		t.Fatal(err)
	}
	base.Close()

	base2, err := OpenBase(filepath.Dir(filepath.Dir(dir)))
	if err != nil {
		t.Fatal(err)
	}
	defer base2.Close()
	coll2, err := base2.Open("t")
	if err != nil {
		t.Fatalf("коллекция не открылась после обрыва: %v", err)
	}
	segs, _ := segmentDirs(dir)
	if len(segs) != 1 || filepath.Base(segs[0]) != "seg-00009" {
		t.Fatalf("подмена не доведена: %v", segs)
	}
	if _, err := os.Stat(filepath.Join(dir, reanalyzeMark)); err == nil {
		t.Fatal("план подмены остался на диске")
	}
	if hits, err := coll2.Search("сортировки", DefaultSearchOpts()); err != nil || len(hits) == 0 {
		t.Fatalf("поиск после восстановления: %v %v", hits, err)
	}
}

// Отмена во время построения оставляет прежний индекс на месте.
func TestReanalyzeCancelLeavesOldIndex(t *testing.T) {
	_, coll := reanalyzeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := coll.Reanalyze(ctx, nil); err == nil {
		t.Fatal("отменённая пересборка обязана вернуть ошибку")
	}
	segs, _ := segmentDirs(coll.Dir())
	if len(segs) == 0 {
		t.Fatal("прежний индекс пропал")
	}
	if hits, err := coll.Search("сортировки", DefaultSearchOpts()); err != nil || len(hits) == 0 {
		t.Fatalf("поиск после отмены: %v %v", hits, err)
	}
}
