package maint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Общая фикстура команд обслуживания: настоящая коллекция с двумя книгами
// и граф при ней. Команды открывают их тем же путём, что и в работе, — через
// config, kb и graph, — поэтому тест ловит то, что видит человек, а не то,
// что видит отдельная функция.
//
// Состояние: книга keep жива; книга gone убрана с диска и помечена удалённой,
// но в графе от неё остались отметки разбора всех её кусков — ровно тот след,
// на котором ошибались счёты доктора. Понятие 3 поглощено склейкой понятием 2.
type maintFixture struct {
	cfg        *config.Config
	name       string
	books      string // каталог книг (корень библиотеки)
	coll       string // каталог коллекции
	dir        string // каталог графа
	keep, gone uint32 // номера живой и удалённой книг
	keepChunks int
	goneChunks int
	keepOrds   []uint32 // номера кусков живой книги
}

// fixtureText — текст книги из lines строк; куска хватает примерно на
// дюжину. Строками, а не одной длинной: строку нарезка не режет, и весь
// текст лёг бы одним куском. Тексты книг различаются темой: одинаковые
// файлы коллекция считает одной книгой.
func fixtureText(topic string, lines int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString("Line ")
		b.WriteString(strings.Repeat("y", i%13+1))
		b.WriteString(" explains ")
		b.WriteString(topic)
		b.WriteString(" in plain words.\n")
	}
	return b.String()
}

func newMaintFixture(t *testing.T) *maintFixture {
	t.Helper()
	root := t.TempDir()
	f := &maintFixture{name: "proba", books: filepath.Join(root, "books")}
	if err := os.MkdirAll(f.books, 0o755); err != nil {
		t.Fatal(err)
	}
	keepPath := filepath.Join(f.books, "keep.txt")
	gonePath := filepath.Join(f.books, "gone.txt")
	if err := os.WriteFile(keepPath, []byte(fixtureText("kubernetes deployment", 120)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gonePath, []byte(fixtureText("goroutines and channels", 300)), 0o644); err != nil {
		t.Fatal(err)
	}

	f.cfg = &config.Config{}
	f.cfg.KB.Dir = filepath.Join(root, "kb")
	f.cfg.KB.Roots = []string{f.books}
	base, err := kb.OpenBase(f.cfg.KB.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	coll, err := base.Create(f.name, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := coll.AddRoots([]string{f.books}); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Add(context.Background(), []string{f.books}, kb.IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	for _, b := range coll.Books() {
		switch filepath.Base(b.Path) {
		case "keep.txt":
			f.keep = b.ID
		case "gone.txt":
			f.gone = b.ID
		}
	}
	if f.keep == 0 || f.gone == 0 {
		t.Fatalf("книги не получили номеров: %+v", coll.Books())
	}
	var keepOrds, goneOrds []uint32
	if err := coll.EachChunkRef(kb.ChunkFilter{}, func(c kb.ChunkRef) error {
		switch c.Doc {
		case f.keep:
			keepOrds = append(keepOrds, c.Ord)
		case f.gone:
			goneOrds = append(goneOrds, c.Ord)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.keepChunks, f.goneChunks = len(keepOrds), len(goneOrds)
	f.keepOrds = keepOrds
	// Мёртвых отметок должно быть больше, чем живых кусков: иначе прежний
	// счёт «разобрано» по всем отметкам не дорастал бы до «осталось 0».
	if f.keepChunks < 4 || f.goneChunks <= f.keepChunks {
		t.Fatalf("фикстура не того размера: кусков у живой книги %d, у удалённой %d", f.keepChunks, f.goneChunks)
	}
	f.coll = coll.Dir()

	g, err := graph.Create(coll.Dir(), coll.Name(), coll.ChunkCount(), f.cfg.Graph.Rules())
	if err != nil {
		t.Fatal(err)
	}
	f.dir = g.Dir()
	add := func(name, typ string, aliases ...string) uint32 {
		id, _, err := g.Entities().Add(name, typ, aliases...)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	coreDNS := add("CoreDNS", graph.TypeTech)
	kube := add("Kubernetes", graph.TypeTech, "кубер")
	k8s := add("k8s", graph.TypeTech, "кубернетес")
	docker := add("Docker", graph.TypeTech)
	if _, err := g.Merges().Add([]graph.MergeRec{{From: k8s, To: kube, Cos: 0.91, Verdict: "ДА", Why: "фикстура"}}); err != nil {
		t.Fatal(err)
	}
	k0 := graph.ChunkKey{Doc: f.keep, Ord: keepOrds[0]}
	k1 := graph.ChunkKey{Doc: f.keep, Ord: keepOrds[1]}
	for _, m := range []struct {
		id uint32
		k  graph.ChunkKey
	}{{coreDNS, k0}, {kube, k0}, {docker, k1}, {kube, k1}} {
		if err := g.Mentions().Add(m.id, m.k); err != nil {
			t.Fatal(err)
		}
	}
	for _, ed := range []graph.Edge{
		{Src: coreDNS, Dst: kube, Weight: 1, Evidence: k0},
		{Src: docker, Dst: kube, Weight: 1, Evidence: k1},
	} {
		if err := g.Edges().Add(ed); err != nil {
			t.Fatal(err)
		}
	}
	// Живая книга: один кусок разобран. Удалённая — разобрана вся.
	if err := g.Progress().Mark(k0, graph.MarkDone); err != nil {
		t.Fatal(err)
	}
	for _, ord := range goneOrds {
		if err := g.Progress().Mark(graph.ChunkKey{Doc: f.gone, Ord: ord}, graph.MarkDone); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	// Книга убрана с диска и помечена удалённой.
	if err := os.Remove(gonePath); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Sync(context.Background(), kb.IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(coll.DeletedBooks()) != 1 {
		t.Fatalf("удалённых книг %d, ожидалась 1", len(coll.DeletedBooks()))
	}
	return f
}

// markMixed добавляет отметки всех видов, на которых расходились счёты:
// у живой книги кусок 1 модель не разобрала, кусок 2 — «служебный» без
// признака оглавления (забыт чисткой, сборка возьмёт его снова); и 40 отметок
// книги, которой нет даже в хранилище кусков (коллекцию уплотнили).
// После этого у живой книги разобрано 3 куска, а сборка возьмёт keepChunks-2.
func (f *maintFixture) markMixed(t *testing.T) {
	t.Helper()
	g, err := graph.Open(f.coll, 0, f.cfg.Graph.Rules())
	if err != nil {
		t.Fatal(err)
	}
	mark := func(k graph.ChunkKey, m uint32) {
		if err := g.Progress().Mark(k, m); err != nil {
			t.Fatal(err)
		}
	}
	mark(graph.ChunkKey{Doc: f.keep, Ord: f.keepOrds[1]}, graph.MarkSkipped)
	mark(graph.ChunkKey{Doc: f.keep, Ord: f.keepOrds[2]}, graph.MarkService)
	for ord := uint32(0); ord < 40; ord++ {
		mark(graph.ChunkKey{Doc: 999, Ord: ord}, graph.MarkDone)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
}

// dirHash — отпечаток каталога: имена и содержимое всех файлов. Время
// изменения не входит: сухой прогон судится по тому, что лежит на диске.
func dirHash(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		sum := sha256.Sum256(b)
		lines = append(lines, rel+" "+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// dirFiles — имена файлов каталога (без подкаталогов), по алфавиту.
func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}
