package graph

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRegistry кладёт реестр понятий строками, как его пишет сборка.
func writeRegistry(t *testing.T, lines []string) string {
	t.Helper()
	coll := t.TempDir()
	dir := filepath.Join(coll, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, entitiesFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return coll
}

func registryLines(t *testing.T, coll string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(coll, DirName, entitiesFile))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// Двадцать копий одной записи — обычное состояние реестра после недель сборки:
// счётчики понятия обновляются дозаписью. Уплотнение обязано оставить последнюю.
func TestCompactKeepsLastRecord(t *testing.T) {
	coll := writeRegistry(t, []string{
		`{"id":1,"name":"Go","norm":"go","type":"технология","docs":1,"count":1,"at":1}`,
		`{"id":2,"name":"канал","norm":"канал","type":"понятие","docs":1,"count":1,"at":2}`,
		`{"id":1,"name":"Go","norm":"go","type":"технология","docs":7,"count":90,"at":3}`,
	})
	st, err := Compact(coll, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.RecordsBefore != 3 || st.RecordsAfter != 2 {
		t.Fatalf("записей %d → %d, ожидалось 3 → 2", st.RecordsBefore, st.RecordsAfter)
	}
	if len(st.Diffs) != 0 {
		t.Fatalf("словари разошлись на ровном месте: %v", st.Diffs)
	}
	if !st.Applied {
		t.Fatal("реестр не подменён, хотя расхождений нет")
	}
	lines := registryLines(t, coll)
	if len(lines) != 2 || !strings.Contains(lines[0], `"count":90`) {
		t.Fatalf("оставлена не последняя запись: %v", lines)
	}
	if _, err := os.Stat(st.Backup); err != nil {
		t.Fatalf("прежний файл не сохранён: %v", err)
	}
}

// Порядок понятий — по первому появлению: спорный ключ-синоним достаётся тому,
// кто пришёл раньше, и уплотнение не должно менять победителя.
func TestCompactKeepsFirstAppearanceOrder(t *testing.T) {
	coll := writeRegistry(t, []string{
		`{"id":5,"name":"RAG","norm":"rag","type":"понятие","aliases":["поиск"],"at":1}`,
		`{"id":6,"name":"BM25","norm":"bm25","type":"понятие","aliases":["поиск"],"at":2}`,
		`{"id":5,"name":"RAG","norm":"rag","type":"понятие","aliases":["поиск"],"docs":3,"at":3}`,
	})
	st, err := Compact(coll, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Diffs) != 0 {
		t.Fatalf("спорный синоним сменил владельца: %v", st.Diffs)
	}
	lines := registryLines(t, coll)
	if len(lines) != 2 || !strings.Contains(lines[0], `"id":5`) {
		t.Fatalf("порядок первого появления не сохранён: %v", lines)
	}
}

// Проверка ничего не трогает: она нужна именно затем, чтобы посмотреть до того,
// как менять файл, который стоил недель работы видеокарты.
func TestCompactCheckDoesNotTouchFile(t *testing.T) {
	coll := writeRegistry(t, []string{
		`{"id":1,"name":"Go","norm":"go","type":"технология","at":1}`,
		`{"id":1,"name":"Go","norm":"go","type":"технология","docs":9,"at":2}`,
	})
	before := registryLines(t, coll)
	st, err := Compact(coll, "", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Applied {
		t.Fatal("проверка подменила реестр")
	}
	if got := registryLines(t, coll); len(got) != len(before) {
		t.Fatalf("файл изменился при проверке: было %d строк, стало %d", len(before), len(got))
	}
	if st.RecordsBefore != 2 || st.RecordsAfter != 1 {
		t.Fatalf("проверка посчитала неверно: %d → %d", st.RecordsBefore, st.RecordsAfter)
	}
}

// Строка с полем, которого эта версия программы не знает, обязана дойти
// до нового файла целиком: уплотнение переписывает файл, а не его содержимое.
func TestCompactKeepsUnknownFields(t *testing.T) {
	coll := writeRegistry(t, []string{
		`{"id":1,"name":"Go","norm":"go","type":"технология","at":1,"weight_of_tomorrow":42}`,
		`это не JSON и должно быть пропущено`,
	})
	if _, err := Compact(coll, "", false, false); err != nil {
		t.Fatal(err)
	}
	lines := registryLines(t, coll)
	if len(lines) != 1 || !strings.Contains(lines[0], "weight_of_tomorrow") {
		t.Fatalf("незнакомое поле потеряно: %v", lines)
	}
}

// Битая последняя строка — обычный след внезапного выключения. Она пропускается,
// а всё остальное обязано уцелеть.
func TestCompactSurvivesTornTail(t *testing.T) {
	coll := writeRegistry(t, []string{
		`{"id":1,"name":"Go","norm":"go","type":"технология","at":1}`,
		`{"id":2,"name":"канал","norm":"кана`,
	})
	st, err := Compact(coll, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.RecordsAfter != 1 {
		t.Fatalf("оборванная запись попала в реестр: %d", st.RecordsAfter)
	}
}

// Уплотнение с подменой не идёт под живой сборкой и не оставляет признака.
//
// Сборка держит реестр открытым на дозапись: подмени файл под ней, и её
// новые понятия уйдут в копию «.bak-…» (аудит обвязки 17.09.2026, S1).
func TestCompactRefusesUnderLiveBuild(t *testing.T) {
	coll := writeRegistry(t, []string{
		`{"id":1,"name":"Go","norm":"go","type":"технология","docs":1,"count":1,"at":1}`,
		`{"id":1,"name":"Go","norm":"go","type":"технология","docs":7,"count":90,"at":3}`,
	})
	dir := filepath.Join(coll, DirFor(""))
	lock := filepath.Join(dir, lockFile)
	// Признак живой сборки: процесс теста заведомо жив.
	body := fmt.Sprintf("pid %d, начато 2026-09-17T12:00:00+03:00\n", os.Getpid())
	if err := os.WriteFile(lock, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Compact(coll, "", false, false)
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("под живой сборкой ожидался отказ LockedError, получено: %v", err)
	}
	if baks, _ := filepath.Glob(filepath.Join(dir, entitiesFile+".bak-*")); len(baks) != 0 {
		t.Fatalf("реестр подменён под живой сборкой: %v", baks)
	}
	// Проверка без подмены под сборкой допустима: она ничего не пишет.
	if _, err := Compact(coll, "", true, false); err != nil {
		t.Fatalf("проверка под сборкой: %v", err)
	}

	// Признак от мёртвого процесса снимается, а после работы своего не остаётся.
	if err := os.WriteFile(lock, []byte("pid 999999999, начато 2026-09-17T12:00:00+03:00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Compact(coll, "", false, false)
	if err != nil || !st.Applied {
		t.Fatalf("после мёртвого хозяина уплотнение обязано пройти: %+v, %v", st, err)
	}
	if _, err := os.Stat(lock); err == nil {
		t.Fatal("уплотнение оставило за собой признак сборки")
	}
}

// Уплотнение с выбрасыванием мёртвых понятий (этап 90, п. 3): понятие без
// упоминаний, связей и склеек уходит из реестра, остальные и их номера — нет;
// проверка ничего не подменяет, но говорит, сколько было бы выброшено.
func TestCompactDropsDeadEntities(t *testing.T) {
	g := newGraphWith(t, "живое", "мёртвое", "связанное", "поглощающее", "поглощённое")
	live, dead, linked, keep, gone := uint32(1), uint32(2), uint32(3), uint32(4), uint32(5)
	if err := g.Mentions().Add(live, ChunkKey{Doc: 1, Ord: 1}); err != nil {
		t.Fatal(err)
	}
	if err := g.Edges().Add(Edge{Src: live, Dst: linked, Type: RelUses, Weight: 1, Evidence: ChunkKey{Doc: 1, Ord: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Merges().Add([]MergeRec{{From: gone, To: keep}}); err != nil {
		t.Fatal(err)
	}
	got := g.DeadEntities()
	if len(got) != 1 || got[0] != dead {
		t.Fatalf("мёртвыми названы %v, ожидалось только %d", got, dead)
	}
	drop := map[uint32]bool{dead: true}
	collDir := filepath.Dir(g.dir)
	g.Close()

	st, err := CompactDrop(collDir, "", true, false, drop)
	if err != nil {
		t.Fatal(err)
	}
	if st.Applied || st.Dropped != 1 {
		t.Fatalf("проверка: подменено %v, выброшено %d", st.Applied, st.Dropped)
	}
	st, err = CompactDrop(collDir, "", false, false, drop)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Applied || st.Dropped != 1 || st.RecordsAfter != 4 {
		t.Fatalf("уплотнение: подменено %v, выброшено %d, записей %d", st.Applied, st.Dropped, st.RecordsAfter)
	}
	g2, err := Open(collDir, 100, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	if _, ok := g2.Entities().Get(dead); ok {
		t.Fatal("мёртвое понятие осталось в реестре")
	}
	for _, id := range []uint32{live, linked, keep} {
		if _, ok := g2.Entities().Get(id); !ok {
			t.Fatalf("живое понятие %d пропало", id)
		}
	}
	if g2.Merges().Resolve(gone) != keep {
		t.Fatal("склейка потеряна")
	}
	if e, ok := g2.Entities().Get(linked); !ok || e.ID != linked {
		t.Fatal("номера сдвинулись — векторы указали бы не туда")
	}
}
