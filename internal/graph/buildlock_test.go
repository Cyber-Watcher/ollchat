package graph

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildFixture — граф с одним понятием и одним упоминанием: чистке есть что
// убрать, и подмена журналов случится по-настоящему.
func buildFixture(t *testing.T) (coll string, chunk ChunkKey) {
	t.Helper()
	g, coll := graph(t)
	id, _, err := g.Entities().Add("Альфа", TypeConcept)
	if err != nil {
		t.Fatal(err)
	}
	chunk = ChunkKey{Doc: 1, Ord: 1}
	must(t, g.Mentions().Add(id, chunk))
	must(t, g.Progress().Mark(chunk, MarkDone))
	must(t, g.Close())
	return coll, chunk
}

// backups — копии журналов «.bak-…», оставленные подменой.
func backups(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// Сборка занимает признак до чтения журналов: чистка, пришедшая посреди
// открытия, получает отказ, а не подменяет журналы под открывающейся сборкой.
//
// До 07.10.2026 сборка открывала граф (десятки секунд) без замка, чистка
// успевала переименовать журналы в «.bak-…», а сборка затем брала замок
// и часами дописывала в переименованные файлы (аудит, №9).
func TestOpenForBuildLocksBeforeReading(t *testing.T) {
	coll, chunk := buildFixture(t)
	dir := filepath.Join(coll, DirName)

	var forgetErr error
	called := false
	g, err := openForBuild(coll, "books", 1000, Rules{}, CreateOpts{}, func(OpenProgress) {
		if called {
			return
		}
		called = true
		// Другой «процесс» в это время чистит граф.
		_, forgetErr = ForgetChunks(dir, func(k ChunkKey) bool { return k == chunk }, false)
	})
	if err != nil {
		t.Fatalf("открытие для сборки: %v", err)
	}
	defer g.Close()
	if !called {
		t.Fatal("ход открытия не сообщался — проверка ничего не проверила")
	}
	if !errors.Is(forgetErr, ErrLocked) {
		t.Fatalf("чистка посреди открытия сборки: %v, ожидался отказ ErrLocked", forgetErr)
	}
	if b := backups(t, dir); len(b) != 0 {
		t.Fatalf("журналы подменены под сборкой: %v", b)
	}
	if !g.Locked() {
		t.Fatal("граф для сборки открыт без признака сборки")
	}
	// Сборка пишет в живые журналы: дописанное видно после переоткрытия.
	if err := g.Mentions().Add(1, ChunkKey{Doc: 1, Ord: 2}); err != nil {
		t.Fatal(err)
	}
	must(t, g.Close())
	if g.Locked() {
		t.Error("признак сборки не снят при закрытии")
	}
	again, err := Open(coll, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if n := len(again.Mentions().Of(1)); n != 2 {
		t.Errorf("упоминаний после захода %d, ожидалось 2 — запись ушла мимо живого журнала", n)
	}
}

// Граф, открытый без замка, после подмены журналов замок не получает:
// дописывать в переименованные копии хуже, чем честно отказаться.
func TestLockRefusesReplacedJournals(t *testing.T) {
	coll, chunk := buildFixture(t)
	g, err := Open(coll, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, err := ForgetChunks(g.Dir(), func(k ChunkKey) bool { return k == chunk }, false); err != nil {
		t.Fatalf("чистка: %v", err)
	}
	if err := g.Lock(); !errors.Is(err, ErrStale) {
		t.Fatalf("замок на графе с подменёнными журналами: %v, ожидался ErrStale", err)
	}
	if g.Locked() {
		t.Error("после отказа признак сборки остался висеть")
	}
}

// Новый граф заводится уже под признаком, а сборка по нему идёт как обычно
// и снимает признак в конце.
func TestOpenForBuildCreatesAndBuilds(t *testing.T) {
	coll := collection(t)
	g, err := OpenForBuild(coll, "books", 10, Rules{}, CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if !g.Locked() {
		t.Fatal("новый граф для сборки заведён без признака")
	}
	m := &model{answer: func(int) (string, error) { return goodAnswer, nil }}
	res, err := Build(context.Background(), chunksFor(3, "/AI/книга.pdf"), g, m, BuildOpts{Workers: 1}, nil)
	if err != nil {
		t.Fatalf("сборка: %v", err)
	}
	if res.Done != 3 {
		t.Errorf("разобрано %d, ожидалось 3", res.Done)
	}
	if g.Locked() {
		t.Error("признак сборки остался после конца захода")
	}
	// Второй открывающий для сборки, пока первый держит признак, — отказ.
	g2, err := OpenForBuild(coll, "books", 10, Rules{}, CreateOpts{})
	if err != nil {
		t.Fatalf("после конца захода граф для сборки не открылся: %v", err)
	}
	defer g2.Close()
	if _, err := OpenForBuild(coll, "books", 10, Rules{}, CreateOpts{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("вторая сборка при живой первой: %v, ожидался ErrLocked", err)
	}
}
