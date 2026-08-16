package live

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itpro/ollchat/internal/config"
	"github.com/itpro/ollchat/internal/kb"
	"github.com/itpro/ollchat/internal/kbembed"
)

// Живые проверки работают с настоящей базой пользователя, поэтому в обычный
// прогон не входят: включаются переменной, как и тесты против сервера Ollama.
func open(t *testing.T) *kb.Collection {
	if os.Getenv("OLLCHAT_KB_LIVE") == "" {
		t.Skip("нужен OLLCHAT_KB_LIVE=1 и собранная коллекция books")
	}
	base, err := kb.OpenBase(os.Getenv("HOME") + "/.local/share/ollchat/kb")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { base.Close() })
	c, err := base.Open("books")
	if err != nil {
		t.Skip(err)
	}
	return c
}

// TestRealSearch — качество поиска на настоящей библиотеке.
func TestRealSearch(t *testing.T) {
	c := open(t)
	for _, q := range []string{"goroutine канал", "kubernetes pod", "буферизированный канал", "SQL инъекция"} {
		start := time.Now()
		res, err := c.Search(q, kb.SearchOpts{TopK: 3})
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		t.Logf("── %q: найдено %d за %s", q, len(res), time.Since(start).Round(time.Millisecond))
		for _, r := range res {
			snip := trim(strings.ReplaceAll(r.Snippet, "\n", " "), 90)
			t.Logf("   %s · стр. %d · %s", trim(r.Book, 45), r.UnitFrom, snip)
		}
		if len(res) == 0 {
			t.Errorf("по запросу %q ничего не найдено", q)
		}
	}
}

// TestRealMerge — уплотнение на четверти миллиона кусков.
func TestRealMerge(t *testing.T) {
	if os.Getenv("OLLCHAT_MERGE_LIVE") == "" {
		t.Skip("нужен OLLCHAT_MERGE_LIVE=1")
	}
	c := open(t)
	before := c.Stats()
	t.Logf("до: книг %d, кусков %d, сегментов %d, %.1f МБ",
		before.Books, before.Chunks, before.Segments, float64(before.Bytes)/1e6)

	books := c.Books()
	victim := books[len(books)/2]
	t.Logf("убираю книгу: %s", trim(victim.Path, 70))
	if err := c.Forget(victim.Path); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	res, err := c.Merge(context.Background(), func(p kb.Progress) {
		if p.DocsTotal > 0 && p.DocsDone%50000 < 1500 {
			fmt.Fprintf(os.Stderr, "\r%s %d/%d      ", p.Phase, p.DocsDone, p.DocsTotal)
		}
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("уплотнение за %s: кусков %d → %d, сегментов %d → %d, %.1f → %.1f МБ",
		time.Since(start).Round(time.Second), res.ChunksBefore, res.ChunksAfter,
		res.SegmentsBefore, res.SegmentsAfter,
		float64(res.BytesBefore)/1e6, float64(res.BytesAfter)/1e6)

	hits, err := c.Search("goroutine канал", kb.SearchOpts{TopK: 3})
	if err != nil || len(hits) == 0 {
		t.Fatalf("после уплотнения поиск сломался: %v, найдено %d", err, len(hits))
	}
	t.Logf("после уплотнения поиск жив: %s · стр. %d", trim(hits[0].Book, 50), hits[0].UnitFrom)
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// TestSemanticCrossLanguage — приёмка смыслового поиска на настоящей библиотеке.
//
// Запускается, только когда модель эмбеддингов есть на сервере:
//
//	OLLCHAT_KB_LIVE=1 OLLCHAT_EMBED_URL=http://192.168.77.77:11434 \
//	OLLCHAT_EMBED_MODEL=bge-m3 go test ./internal/kb/live/ -run TestSemantic -v
//
// Условие приёмки записано в docs/kb_semantic_search.md: русский запрос обязан
// находить английскую книгу там, где поиск по словам не справляется.
func TestSemanticCrossLanguage(t *testing.T) {
	url, model := os.Getenv("OLLCHAT_EMBED_URL"), os.Getenv("OLLCHAT_EMBED_MODEL")
	if url == "" || model == "" {
		t.Skip("нужны OLLCHAT_EMBED_URL и OLLCHAT_EMBED_MODEL")
	}
	c := open(t)
	cfg := config.Default()
	cfg.KB.EmbedModel, cfg.KB.EmbedURL = model, url
	emb := kbembed.New(cfg, url, 10*time.Minute, nil)
	if emb == nil {
		t.Fatal("эмбеддер не собрался")
	}

	cov := c.Coverage(model)
	t.Logf("покрытие: %d из %d (%d%%)", cov.Covered, cov.Total, cov.Percent())
	if cov.Covered == 0 {
		t.Skip("смыслы не посчитаны: ollchat --kb-embed books")
	}

	cases := []struct{ query, want string }{
		{"буферизированный канал", "go"},
		{"горутина", "go"},
	}
	opt := kb.SearchOpts{TopK: 5, MaxPerDoc: 2, Semantic: true}
	for _, tc := range cases {
		words, err := c.SearchWith(context.Background(), tc.query, opt, nil)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		both, err := c.SearchWith(context.Background(), tc.query, opt, emb)
		if err != nil {
			t.Fatalf("%q: %v", tc.query, err)
		}
		t.Logf("── %q (со смыслом за %s)", tc.query, time.Since(start).Round(time.Millisecond))
		t.Logf("   только слова:")
		for _, r := range words {
			t.Logf("     %s · стр. %d", trim(r.Book, 52), r.UnitFrom)
		}
		t.Logf("   слова + смысл:")
		for _, r := range both {
			t.Logf("     %s · стр. %d", trim(r.Book, 52), r.UnitFrom)
		}
		if note := c.SearchNote(); note != "" {
			t.Logf("   заметка: %s", note)
		}
		if len(both) == 0 {
			t.Errorf("по запросу %q не найдено ничего", tc.query)
		}
	}
}

// TestSemanticSideBySide — честное сравнение на разных запросах.
//
// Нужно, чтобы не выдавать желаемое за действительное: смысловой поиск помогает
// не везде. Там, где слово запроса буквально есть в книгах, BM25 справляется сам.
func TestSemanticSideBySide(t *testing.T) {
	url, model := os.Getenv("OLLCHAT_EMBED_URL"), os.Getenv("OLLCHAT_EMBED_MODEL")
	if url == "" || model == "" {
		t.Skip("нужны OLLCHAT_EMBED_URL и OLLCHAT_EMBED_MODEL")
	}
	c := open(t)
	cfg := config.Default()
	cfg.KB.EmbedModel, cfg.KB.EmbedURL = model, url
	emb := kbembed.New(cfg, url, 10*time.Minute, nil)

	queries := []string{
		"буферизированный канал",
		"как остановить утечку памяти",
		"чем отличается процесс от потока",
		"перехват паники и восстановление",
		"почему запрос к базе выполняется медленно",
		"права доступа к файлам в контейнере",
	}
	opt := kb.SearchOpts{TopK: 3, MaxPerDoc: 1, Semantic: true}
	for _, q := range queries {
		words, err := c.SearchWith(context.Background(), q, opt, nil)
		if err != nil {
			t.Fatal(err)
		}
		both, err := c.SearchWith(context.Background(), q, opt, emb)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("── %q", q)
		for i := 0; i < 3; i++ {
			var a, b string
			if i < len(words) {
				a = trim(words[i].Book, 38)
			}
			if i < len(both) {
				b = trim(both[i].Book, 38)
			}
			t.Logf("   %-40s | %s", a, b)
		}
	}
}

// TestSemanticCosines показывает, насколько сильны совпадения по смыслу.
// Нужен, чтобы решить, стоит ли отсекать слабые: они вытесняют хорошие
// словесные попадания, ничего не давая взамен.
func TestSemanticCosines(t *testing.T) {
	url, model := os.Getenv("OLLCHAT_EMBED_URL"), os.Getenv("OLLCHAT_EMBED_MODEL")
	if url == "" || model == "" {
		t.Skip("нужны OLLCHAT_EMBED_URL и OLLCHAT_EMBED_MODEL")
	}
	c := open(t)
	cfg := config.Default()
	cfg.KB.EmbedModel, cfg.KB.EmbedURL = model, url
	emb := kbembed.New(cfg, url, 10*time.Minute, nil)

	for _, q := range []string{
		"буферизированный канал",
		"перехват паники и восстановление",
		"почему запрос к базе выполняется медленно",
	} {
		hits, err := c.DebugVectorHits(context.Background(), emb, q, 6)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("── %q", q)
		for _, h := range hits {
			t.Logf("   косинус %.3f · %s · стр. %d", h.Score, trim(h.Book, 45), h.UnitFrom)
		}
	}
}

// TestSemanticRRF подбирает постоянную слияния на настоящей библиотеке.
//
// При большом k кусок, средний в обоих списках, обходит отличный в одном —
// награда за согласие списков перевешивает качество попадания. Насколько это
// вредно, видно только на живом корпусе.
func TestSemanticRRF(t *testing.T) {
	url, model := os.Getenv("OLLCHAT_EMBED_URL"), os.Getenv("OLLCHAT_EMBED_MODEL")
	if url == "" || model == "" {
		t.Skip("нужны OLLCHAT_EMBED_URL и OLLCHAT_EMBED_MODEL")
	}
	c := open(t)
	cfg := config.Default()
	cfg.KB.EmbedModel, cfg.KB.EmbedURL = model, url
	emb := kbembed.New(cfg, url, 10*time.Minute, nil)

	queries := []string{
		"буферизированный канал",
		"перехват паники и восстановление",
		"чем отличается процесс от потока",
		"почему запрос к базе выполняется медленно",
	}
	opt := kb.SearchOpts{TopK: 3, MaxPerDoc: 1, Semantic: true}
	for _, k := range []float64{60, 20, 10, 5} {
		kb.SetRRFK(k)
		t.Logf("════ k = %.0f ════", k)
		for _, q := range queries {
			hits, err := c.SearchWith(context.Background(), q, opt, emb)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(hits))
			for _, h := range hits {
				names = append(names, trim(h.Book, 30))
			}
			t.Logf("  %-42s → %s", trim(q, 40), strings.Join(names, " | "))
		}
	}
	kb.SetRRFK(60)
}

// TestSemanticMinCosine подбирает порог близости на живой коллекции.
//
// Замысел: слабое совпадение по смыслу не должно вытеснять сильное словесное.
// Вопрос в том, различаются ли полезные и вредные попадания по косинусу вообще —
// а это видно только на настоящем корпусе.
func TestSemanticMinCosine(t *testing.T) {
	url, model := os.Getenv("OLLCHAT_EMBED_URL"), os.Getenv("OLLCHAT_EMBED_MODEL")
	if url == "" || model == "" {
		t.Skip("нужны OLLCHAT_EMBED_URL и OLLCHAT_EMBED_MODEL")
	}
	c := open(t)
	cfg := config.Default()
	cfg.KB.EmbedModel, cfg.KB.EmbedURL = model, url
	emb := kbembed.New(cfg, url, 10*time.Minute, nil)

	queries := []string{
		"буферизированный канал",
		"перехват паники и восстановление",
		"почему запрос к базе выполняется медленно",
		"как остановить утечку памяти",
		"чем отличается процесс от потока",
		"права доступа к файлам в контейнере",
	}
	base := kb.SearchOpts{TopK: 3, MaxPerDoc: 1, Semantic: true}

	// Сначала — только слова, для отсчёта.
	t.Log("════ только слова ════")
	for _, q := range queries {
		hits, err := c.SearchWith(context.Background(), q, base, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("  %-42s → %s", trim(q, 40), names(hits))
	}
	for _, min := range []float64{0, 0.55, 0.60, 0.65, 0.70} {
		t.Logf("════ порог косинуса %.2f ════", min)
		opt := base
		opt.MinCosine = min
		for _, q := range queries {
			hits, err := c.SearchWith(context.Background(), q, opt, emb)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("  %-42s → %s", trim(q, 40), names(hits))
		}
	}
}

func names(hits []kb.Result) string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, trim(h.Book, 26))
	}
	if len(out) == 0 {
		return "(пусто)"
	}
	return strings.Join(out, " | ")
}

// probe — запрос и книга, которая для него считается верным ответом.
//
// Оценка на глаз по названиям книг — плохая опора: легко увидеть то, что
// хочется. Поэтому ожидаемая книга записана заранее, до замера, и считается
// простая мера: попала ли она в первые три места.
type probe struct{ query, want string }

var probes = []probe{
	{"буферизированный канал", "Go на практике"},
	{"перехват паники и восстановление", "Anatomy of Go"},
	{"чем отличается процесс от потока", "CLR via C#"},
	{"почему запрос к базе выполняется медленно", "PostgreSQL"},
	{"права доступа к файлам в контейнере", "Безопасность контейнеров"},
	{"сборка мусора и её настройка", "CLR via C#"},
	{"как устроен планировщик задач", "Go на практике"},
	{"шифрование трафика между службами", "Безопасность"},
}

// TestSemanticWeights подбирает вес смыслового списка против словесного.
func TestSemanticWeights(t *testing.T) {
	url, model := os.Getenv("OLLCHAT_EMBED_URL"), os.Getenv("OLLCHAT_EMBED_MODEL")
	if url == "" || model == "" {
		t.Skip("нужны OLLCHAT_EMBED_URL и OLLCHAT_EMBED_MODEL")
	}
	c := open(t)
	cfg := config.Default()
	cfg.KB.EmbedModel, cfg.KB.EmbedURL = model, url
	emb := kbembed.New(cfg, url, 10*time.Minute, nil)

	score := func(w float64, useEmb bool) (int, []string) {
		hit, miss := 0, []string{}
		for _, p := range probes {
			opt := kb.SearchOpts{TopK: 3, MaxPerDoc: 1, Semantic: useEmb, SemanticWeight: w}
			var e kb.Embedder
			if useEmb {
				e = emb
			}
			res, err := c.SearchWith(context.Background(), p.query, opt, e)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, r := range res {
				if strings.Contains(r.Book, p.want) || strings.Contains(r.Path, p.want) {
					found = true
				}
			}
			if found {
				hit++
			} else {
				miss = append(miss, trim(p.query, 34))
			}
		}
		return hit, miss
	}

	n, miss := score(0, false)
	t.Logf("только слова:        %d из %d   мимо: %s", n, len(probes), strings.Join(miss, ", "))
	for _, w := range []float64{1.0, 0.7, 0.5, 0.3, 0.15} {
		n, miss := score(w, true)
		t.Logf("вес смысла %.2f:      %d из %d   мимо: %s", w, n, len(probes), strings.Join(miss, ", "))
	}
}
