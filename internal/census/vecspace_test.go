package census

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/graph/vecstand"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// fakeEmbedder — эмбеддер без сервера: вектор складывается из байтов текста.
type fakeEmbedder struct {
	model string
	dim   int
}

func (f fakeEmbedder) Model() string { return f.model }

func (f fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dim)
		v[0] = 1
		for j := 0; j < len(t); j++ {
			v[j%f.dim] += float32(t[j])
		}
		out[i] = v
	}
	return out, nil
}

// newVectorFixture — коллекция с книгой и векторами кусков модели chunkModel
// и граф при ней с векторами понятий модели entModel; размерность у обоих
// одна — 1024, как у bge-m3, чтобы расходилась только модель.
func newVectorFixture(t *testing.T, chunkModel, entModel string) (*config.Config, string) {
	t.Helper()
	const dim = 1024
	root := t.TempDir()
	books := filepath.Join(root, "books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for i := 0; i < 60; i++ {
		text.WriteString("Relation extraction (RE) finds relations between entities, line ")
		text.WriteString(strings.Repeat("y", i%11+1))
		text.WriteString(".\n")
	}
	if err := os.WriteFile(filepath.Join(books, "re.txt"), []byte(text.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.KB.Dir = filepath.Join(root, "kb")
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	coll, err := base.Create("proba", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := coll.AddRoots([]string{books}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := coll.Add(ctx, []string{books}, kb.IndexOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Embed(ctx, fakeEmbedder{model: chunkModel, dim: dim}, kb.EmbedOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	doc := coll.Books()[0].ID

	g, err := graph.Create(coll.Dir(), coll.Name(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		t.Fatal(err)
	}
	re, _, err := g.Entities().Add("Relation extraction", graph.TypeConcept, "RE")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Mentions().Add(re, graph.ChunkKey{Doc: doc, Ord: 0}); err != nil {
		t.Fatal(err)
	}
	data := make([]int8, dim)
	for i := range data {
		data[i] = int8(i%5 + 1)
	}
	if err := g.SaveEntityVectors(entModel, "", dim, data); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	return cfg, coll.Name()
}

// Переписи misattrib-* не сравнивают векторы разных моделей (аудит
// 07.10.2026, раздел 4.6). Прежде entities.vec читался своей копией формата,
// проверялась только размерность 1024, и векторы понятий другой модели той
// же размерности давали косинусы-мусор — а по ним составлялся список кусков
// для разрушительной --graph-forget-chunks.
func TestMisattribRefusesMixedVectorSpaces(t *testing.T) {
	cfg, name := newVectorFixture(t, "bge-m3", "nomic-embed-text")
	out := filepath.Join(t.TempDir(), "lists")
	runs := []struct {
		mode string
		run  func() error
	}{
		{"misattrib-acronym", func() error { return misattribAcronym(io.Discard, cfg, name, out, 0, 600) }},
		{"misattrib-alias", func() error { return misattribAlias(io.Discard, cfg, name, out, 8, 0.10) }},
		{"misattrib-mention", func() error { return misattribMention(io.Discard, cfg, name, out, 20, 0.08) }},
	}
	for _, r := range runs {
		err := r.run()
		if err == nil || !strings.Contains(err.Error(), "nomic-embed-text") || !strings.Contains(err.Error(), "bge-m3") {
			t.Errorf("%s: векторы разных моделей приняты (%v)", r.mode, err)
		}
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("список кусков к забыванию записан по векторам разных моделей: %v", err)
	}
}

// Векторы одной модели принимаются, и перепись проходит до конца.
func TestMisattribAcceptsSameVectorSpace(t *testing.T) {
	cfg, name := newVectorFixture(t, "bge-m3", "bge-m3")
	if err := misattribAcronym(io.Discard, cfg, name, "", 0, 600); err != nil {
		t.Fatalf("перепись на векторах одной модели: %v", err)
	}
}

func TestSameVectorSpace(t *testing.T) {
	ents := &vecstand.Vectors{Model: "bge-m3", Dim: 1024, Count: 3}
	cases := []struct {
		chunks kb.VecMeta
		ok     bool
		why    string
	}{
		{kb.VecMeta{Model: "bge-m3", Dim: 1024, Count: 10}, true, "та же модель и размерность"},
		{kb.VecMeta{Model: "nomic-embed-text", Dim: 1024, Count: 10}, false, "другая модель той же размерности"},
		{kb.VecMeta{Model: "bge-m3", Dim: 768, Count: 10}, false, "другая размерность"},
		{kb.VecMeta{}, false, "векторов кусков нет"},
	}
	for _, c := range cases {
		if err := sameVectorSpace(c.chunks, ents); (err == nil) != c.ok {
			t.Errorf("%s: ошибка %v", c.why, err)
		}
	}
}
