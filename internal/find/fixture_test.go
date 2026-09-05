package find

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

// Регрессия качества поиска без сети и карты (этап 91, R7.1–R7.2).
//
// Фикстура: десять коротких текстов в testdata/fixture и тридцать вопросов
// с ожидаемой книгой. Векторы для них посчитаны один раз настоящей моделью
// и лежат в vectors.json: тест подставляет их через фальшивый эмбеддер,
// поэтому идёт без сети. Пересчитать векторы:
//
//	OLLCHAT_FIXTURE_RECORD=http://ollama.example:11434 go test ./internal/find/ -run Fixture
//
// Пороги ниже — числа первого прогона; правка слияния, надбавок или
// нарезки, уронившая их, ловится здесь, а не на живой библиотеке.
const (
	fixtureModel   = "bge-m3:latest"
	fixtureRecallK = 8
	// Пороги записаны по первому прогону (см. search-entry-baseline.md).
	wantRecallFusion = 1.0  // первый прогон 04.09.2026: 1.000 (30 вопросов, K=8)
	wantMRRFusion    = 0.96 // первый прогон: 0.967
	wantRecallWords  = 1.0  // первый прогон: 1.000 (MRR 0.957)
)

type fixtureQuestions struct {
	Case []kb.EvalCase `toml:"case"`
}

// vectorBook — text → вектор; ключ — текст в точности как его видит эмбеддер.
type vectorBook map[string][]float32

// fakeEmbedder отдаёт записанные векторы; незнакомый текст — ошибка, а не ноль:
// молчаливый ноль сделал бы тест бессмысленным, не сломав его.
type fakeEmbedder struct {
	book vectorBook
	miss []string
}

func (f *fakeEmbedder) Model() string { return fixtureModel }
func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, ok := f.book[t]
		if !ok {
			f.miss = append(f.miss, t)
			return nil, fmt.Errorf("в vectors.json нет вектора для текста %.60q — пересчитайте фикстуру", t)
		}
		out[i] = v
	}
	return out, nil
}

// recorder записывает всё, что спросили у настоящего эмбеддера.
type recorder struct {
	real kb.Embedder
	book vectorBook
}

func (r *recorder) Model() string { return r.real.Model() }
func (r *recorder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	vecs, err := r.real.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	for i, t := range texts {
		r.book[t] = vecs[i]
	}
	return vecs, nil
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("testdata/fixture")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// buildFixture собирает коллекцию из текстов фикстуры во временной базе.
func buildFixture(t *testing.T, emb kb.Embedder) *kb.Collection {
	t.Helper()
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll, err := base.Create("fixture", "фикстура поиска")
	if err != nil {
		t.Fatal(err)
	}
	dir := fixtureDir(t)
	var paths []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)
	if _, err := coll.Add(context.Background(), paths, kb.IndexOpts{Workers: 1}, nil); err != nil {
		t.Fatalf("индексация фикстуры: %v", err)
	}
	if _, err := coll.Embed(context.Background(), emb, kb.EmbedOpts{Batch: 8, Workers: 1}, nil); err != nil {
		t.Fatalf("векторы фикстуры: %v", err)
	}
	return coll
}

func loadQuestions(t *testing.T) []kb.EvalCase {
	t.Helper()
	var q fixtureQuestions
	if _, err := toml.DecodeFile(filepath.Join(fixtureDir(t), "questions.toml"), &q); err != nil {
		t.Fatal(err)
	}
	return q.Case
}

// evalFixture считает полноту и MRR через то же ядро, что и работа.
func evalFixture(t *testing.T, coll *kb.Collection, emb kb.Embedder, mode kb.EvalMode) kb.EvalReport {
	t.Helper()
	opt := kb.EvalOpts{K: fixtureRecallK, MaxPerDoc: 3}
	opt.Search = func(ctx context.Context, query string, so kb.SearchOpts, want int) ([]kb.Result, error) {
		fo := Opts{Mode: "eval", TopK: want, MaxPerBook: so.MaxPerDoc, Semantic: so.Semantic,
			SemanticOnly: so.SemanticOnly, TableBoost: so.TableBoost, QueryTimeout: 10 * time.Second}
		hits, _, err := Books(ctx, Deps{Coll: coll, Embedder: emb}, query, query, fo)
		return hits, err
	}
	rep, err := coll.Eval(context.Background(), loadQuestions(t), mode, opt, emb)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestFixtureRecord пересчитывает векторы настоящей моделью и переписывает
// vectors.json. Запускается только с OLLCHAT_FIXTURE_RECORD=<адрес сервера>.
func TestFixtureRecord(t *testing.T) {
	url := os.Getenv("OLLCHAT_FIXTURE_RECORD")
	if url == "" {
		t.Skip("нужен OLLCHAT_FIXTURE_RECORD=<адрес Ollama с bge-m3>")
	}
	real := kbembed.New(kbembed.Options{URL: url, Model: fixtureModel, Timeout: 5 * time.Minute}, "", 0, nil)
	if real == nil {
		t.Fatal("эмбеддер не собрался")
	}
	rec := &recorder{real: real, book: vectorBook{}}
	coll := buildFixture(t, rec)
	// Вопросы тоже векторизуются: их векторы нужны тесту.
	for _, mode := range []kb.EvalMode{kb.EvalFusion, kb.EvalSemantic} {
		rep := evalFixture(t, coll, rec, mode)
		t.Logf("%s: recall@%d %.3f, MRR %.3f, nDCG %.3f, промахов %d", mode, fixtureRecallK, rep.Recall, rep.MRR, rep.NDCG, len(rep.Missed))
	}
	data, err := json.Marshal(rec.book)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir(t), "vectors.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("записано векторов: %d (%d КБ)", len(rec.book), len(data)/1024)
}

// TestRecallDoesNotDrop — полнота и MRR на фикстуре не ниже записанных.
func TestRecallDoesNotDrop(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtureDir(t), "vectors.json"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("vectors.json ещё не записан: OLLCHAT_FIXTURE_RECORD=<адрес> go test ./internal/find/ -run FixtureRecord")
	}
	if err != nil {
		t.Fatal(err)
	}
	book := vectorBook{}
	if err := json.Unmarshal(raw, &book); err != nil {
		t.Fatal(err)
	}
	emb := &fakeEmbedder{book: book}
	coll := buildFixture(t, emb)

	fusion := evalFixture(t, coll, emb, kb.EvalFusion)
	words := evalFixture(t, coll, emb, kb.EvalLexical)
	t.Logf("слияние: recall@%d %.3f, MRR %.3f; слова: recall %.3f, MRR %.3f; промахов %d/%d",
		fixtureRecallK, fusion.Recall, fusion.MRR, words.Recall, words.MRR, len(fusion.Missed), len(words.Missed))
	if len(emb.miss) > 0 {
		t.Fatalf("фикстура устарела: нет векторов для %d текстов, первый %.60q", len(emb.miss), emb.miss[0])
	}
	if fusion.Recall < wantRecallFusion || fusion.MRR < wantMRRFusion {
		t.Fatalf("слияние просело: recall %.3f (порог %.3f), MRR %.3f (порог %.3f); промахи: %v",
			fusion.Recall, wantRecallFusion, fusion.MRR, wantMRRFusion, fusion.Missed)
	}
	if words.Recall < wantRecallWords {
		t.Fatalf("поиск по словам просел: recall %.3f (порог %.3f); промахи: %v", words.Recall, wantRecallWords, words.Missed)
	}
	if fusion.Recall < words.Recall {
		t.Fatalf("слияние хуже одних слов: %.3f против %.3f — смысл вредит, а не помогает", fusion.Recall, words.Recall)
	}
}
