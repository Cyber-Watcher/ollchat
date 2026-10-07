package graph

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// appendBytes дописывает в файл журнала сырые байты — так выглядит обрывок
// записи, оставленный kill -9 посреди сброса буфера.
func appendBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// Обрывок в хвосте каждого журнала срезается перед дозаписью: после обрыва,
// переоткрытия и новых записей журналы читаются ровно тем, что записано.
//
// До 07.10.2026 новые записи ложились после обрывка: двоичные журналы
// читались со сдвигом (мусорные номера понятий и кусков), а первая новая
// строка реестра склеивалась с обрывком и пропадала (аудит, №3).
func TestTornTailsCutBeforeAppend(t *testing.T) {
	coll := collection(t)
	rules := Rules{Name: "lab", Format: FormatV2}
	g, err := CreateKind(coll, "books", 1000, rules, CreateOpts{Kind: KindExperimental})
	if err != nil {
		t.Fatal(err)
	}
	dir := g.Dir()
	a, _, _ := g.Entities().Add("Альфа", TypeConcept)
	b, _, _ := g.Entities().Add("Бета", TypeConcept)
	first := ChunkKey{Doc: 1, Ord: 1}
	must(t, g.Mentions().Add(a, first))
	must(t, g.Edges().Add(Edge{Src: a, Dst: b, Type: RelUses, Evidence: first}))
	must(t, g.Progress().Mark(first, MarkDone))
	if _, err := g.Aliases().Add(a, first, "alpha"); err != nil {
		t.Fatal(err)
	}
	must(t, g.Close())

	// Обрывки: короче записи у каждого двоичного журнала, недописанная
	// строка — у реестра.
	appendBytes(t, filepath.Join(dir, mentionsFile), []byte{9, 0, 0, 0, 7})
	appendBytes(t, filepath.Join(dir, edgesFile), []byte{1, 2, 3, 4, 5, 6, 7})
	appendBytes(t, filepath.Join(dir, progressFile), []byte{1, 0, 0, 0})
	appendBytes(t, filepath.Join(dir, aliasesFile), []byte{2, 0, 0, 0, 1, 0, 5, 0, 0})
	appendBytes(t, filepath.Join(dir, entitiesFile), []byte(`{"id":3,"name":"обры`))

	g, err = Open(coll, 1000, rules)
	if err != nil {
		t.Fatal(err)
	}
	// Сборка берёт замок и пишет; хвост каждого журнала приводится в порядок
	// перед первой записью в него.
	must(t, g.Lock())
	c, isNew, err := g.Entities().Add("Гамма", TypeConcept)
	if err != nil || !isNew || c != 3 {
		t.Fatalf("новое понятие: %d, %v, %v — ожидался №3", c, isNew, err)
	}
	second := ChunkKey{Doc: 2, Ord: 1}
	must(t, g.Mentions().Add(c, second))
	must(t, g.Edges().Add(Edge{Src: c, Dst: a, Type: RelPart, Evidence: second}))
	must(t, g.Progress().Mark(second, MarkEmpty))
	if _, err := g.Aliases().Add(c, second, "gamma"); err != nil {
		t.Fatal(err)
	}
	if notes := g.TornTails(); len(notes) != 5 {
		t.Errorf("исправлений хвостов %d, ожидалось 5 — по одному на журнал: %q", len(notes), notes)
	}
	must(t, g.Unlock())
	must(t, g.Close())

	g, err = Open(coll, 1000, rules)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	var names []string
	for _, e := range g.Entities().All() {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "Альфа,Бета,Гамма" {
		t.Errorf("реестр после дозаписи: %q", names)
	}
	if n := g.Mentions().Count(); n != 2 {
		t.Errorf("упоминаний %d, ожидалось 2", n)
	}
	if got := g.Mentions().Of(c); len(got) != 1 || got[0] != second {
		t.Errorf("упоминания нового понятия: %v", got)
	}
	if got := g.Mentions().In(first); len(got) != 1 || got[0] != a {
		t.Errorf("прежнее упоминание: %v", got)
	}
	if n := g.Edges().Count(); n != 2 {
		t.Errorf("связей %d, ожидалось 2", n)
	}
	if got := g.Edges().ofRaw(c); len(got) != 1 || got[0].Dst != a || got[0].Evidence != second {
		t.Errorf("новая связь прочитана не так: %+v", got)
	}
	if n := g.Progress().Count(); n != 2 {
		t.Errorf("отметок %d, ожидалось 2", n)
	}
	if m, ok := g.Progress().MarkOf(second); !ok || m != MarkEmpty {
		t.Errorf("новая отметка: %d, %v", m, ok)
	}
	al := g.Aliases().All()
	if len(al) != 2 || al[1].Entity != c || al[1].Chunk != second || al[1].Norm != "gamma" {
		t.Errorf("синонимы после дозаписи: %+v", al)
	}
}

// Без замка хвост правит первая запись: дозапись после обрывка невозможна
// ни при каком порядке вызовов.
func TestTornTailCutOnFirstWrite(t *testing.T) {
	g, coll := graph(t)
	dir := g.Dir()
	a, _, _ := g.Entities().Add("Альфа", TypeConcept)
	must(t, g.Mentions().Add(a, ChunkKey{Doc: 1, Ord: 1}))
	must(t, g.Close())
	appendBytes(t, filepath.Join(dir, mentionsFile), []byte{0xff, 0xff, 0xff})

	g, err := Open(coll, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	must(t, g.Mentions().Add(a, ChunkKey{Doc: 1, Ord: 2}))
	must(t, g.Close())

	fi, err := os.Stat(filepath.Join(dir, mentionsFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 2*mentionSize {
		t.Fatalf("журнал упоминаний %d байт, ожидалось %d — обрывок остался", fi.Size(), 2*mentionSize)
	}
	g, err = Open(coll, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if got := g.Mentions().Of(a); len(got) != 2 {
		t.Errorf("упоминания после дозаписи: %v", got)
	}
}

// Целая запись реестра, которой недостало лишь перевода строки, не срезается:
// она читается — значит, она часть графа. Ей дописывается перевод строки,
// и следующая запись ложится отдельной строкой.
func TestRegistryLineWithoutNewlineKept(t *testing.T) {
	g, coll := graph(t)
	dir := g.Dir()
	a, _, _ := g.Entities().Add("Альфа", TypeConcept)
	must(t, g.Close())
	appendBytes(t, filepath.Join(dir, entitiesFile), []byte(`{"id":2,"name":"Бета","norm":"бета","type":"понятие","at":1}`))

	g, err := Open(coll, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Entities().Lookup("Бета"); !ok {
		t.Fatal("целая запись без перевода строки не прочиталась")
	}
	c, _, err := g.Entities().Add("Гамма", TypeConcept)
	if err != nil || c != 3 {
		t.Fatalf("новое понятие: %d, %v", c, err)
	}
	must(t, g.Close())

	g, err = Open(coll, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, name := range []string{"Альфа", "Бета", "Гамма"} {
		if _, ok := g.Entities().Lookup(name); !ok {
			t.Errorf("после дозаписи потеряно понятие %q", name)
		}
	}
	if g.Entities().Count() != 3 || a != 1 {
		t.Errorf("понятий %d, ожидалось 3", g.Entities().Count())
	}
}

// Журнал дописал другой процесс после того, как граф был открыт: сборка
// по такому графу не идёт и не пишет ни записи, иначе дозапись по устаревшему
// состоянию в памяти выдала бы уже занятые номера понятий, а срез хвоста
// снёс бы чужие записи.
func TestStaleGraphRefusesAppend(t *testing.T) {
	g, coll := graph(t)
	a, _, _ := g.Entities().Add("Альфа", TypeConcept)
	must(t, g.Close())

	stale, err := Open(coll, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()

	other, err := Open(coll, 1000, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	must(t, other.Mentions().Add(a, ChunkKey{Doc: 1, Ord: 1}))
	must(t, other.Close())
	path := filepath.Join(stale.Dir(), mentionsFile)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	m := &model{answer: func(int) (string, error) { return goodAnswer, nil }}
	_, err = Build(context.Background(), chunksFor(2, "/AI/книга.pdf"), stale, m, BuildOpts{Workers: 1}, nil)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("сборка по устаревшему графу: %v, ожидался ErrStale", err)
	}
	if stale.Locked() {
		t.Error("после отказа признак сборки остался висеть")
	}
	if err := stale.Mentions().Add(a, ChunkKey{Doc: 9, Ord: 9}); !errors.Is(err, ErrStale) {
		t.Fatalf("запись в устаревший журнал: %v, ожидался ErrStale", err)
	}
	if after, _ := os.Stat(path); after.Size() != before.Size() {
		t.Errorf("журнал упоминаний изменился: %d → %d байт", before.Size(), after.Size())
	}
}
