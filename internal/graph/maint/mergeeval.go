package maint

// Замер правила слияния выдержек книг с подтверждениями графа (этап 112, П7).
//
// `find.Search` — ядро `/search` и инструмента `search` — сливает два списка:
// найденное по книгам и куски-подтверждения связей графа. Сейчас граф
// дописывается в хвост и обрезается по TopK вместе с книгами, то есть место
// получает, только когда книги дали меньше TopK кусков. Книга («The Ultimate
// AI Guide for Linux Engineers», 2026, стр. 202–203) советует RRF с k = 60;
// внутри поиска по книгам у нас k = 20 (`kb.DefaultRRFK`), и 60 там оказалось
// вредным. Для слияния именно этих двух списков ни одно число не мерили.
//
// **Что меряется.** Золотой набор (`chunk_id` у каждого вопроса): тот же поиск,
// что у `/search`, один раз на вопрос; затем выдача сливается заново по каждому
// правилу (`find.Remerge`) и считается место эталонного куска. Правило 0 —
// как в работе; его пересливание обязано совпасть с выдачей Search — число
// расхождений печатается как проверка самого прибора.
//
// Граф открывается один раз; карта нужна на вектор вопроса и реранкер.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
)

// MergeEvalOpts — что задаёт `--kb-eval … --kb-eval-graph-merge`.
type MergeEvalOpts struct {
	Collection string
	// Ks — правила слияния: 0 — дописывание (как в работе), > 0 — RRF с этой k.
	Ks    []float64
	Limit int
	// Out — файл строк JSON: по вопросу место эталона в книгах, в графе и по правилам.
	Out string
}

// ParseMergeKs разбирает «0,20,60». Правило 0 добавляется первым, если его нет:
// без него сравнивать не с чем.
func ParseMergeKs(s string) ([]float64, error) {
	ks := []float64{0}
	for f := range strings.SplitSeq(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		k, err := strconv.ParseFloat(f, 64)
		if err != nil || k < 0 {
			return nil, fmt.Errorf("--kb-eval-graph-merge: %q — не неотрицательное число", f)
		}
		if k != 0 {
			ks = append(ks, k)
		}
	}
	return ks, nil
}

// mergeRow — строка JSON на вопрос.
type mergeRow struct {
	N       int            `json:"n"`
	Query   string         `json:"query"`
	Gold    string         `json:"gold"`
	InBooks int            `json:"in_books"` // место в списке книг до слияния, 0 — нет
	InGraph int            `json:"in_graph"` // место среди подтверждений графа, 0 — нет
	Places  map[string]int `json:"places"`   // правило → место в слитой выдаче, 0 — нет
	FromG   map[string]int `json:"from_graph"`
}

func mergeArm(k float64) string {
	if k == 0 {
		return "хвост"
	}
	return "rrf" + strconv.FormatFloat(k, 'f', -1, 64)
}

// MergeEval прогоняет золотой набор через слитый поиск и сравнивает правила слияния.
func MergeEval(stdout io.Writer, cfg *config.Config, setPath string, o MergeEvalOpts) error {
	var set struct {
		Case []kb.EvalCase `toml:"case"`
	}
	if _, err := toml.DecodeFile(setPath, &set); err != nil {
		return fmt.Errorf("набор %s: %w", setPath, err)
	}
	var cases []kb.EvalCase
	skipped := 0
	for _, c := range set.Case {
		if c.ChunkID == "" {
			skipped++
			continue
		}
		cases = append(cases, c)
	}
	if o.Limit > 0 && o.Limit < len(cases) {
		cases = cases[:o.Limit]
	}
	if len(cases) == 0 {
		return fmt.Errorf("в наборе %s нет вопросов с chunk_id", setPath)
	}
	if len(o.Ks) == 0 {
		o.Ks = []float64{0}
	}

	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	name := o.Collection
	if name == "" {
		name = cfg.KB.Default
	}
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "открываю граф коллекции %s…\n", name)
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	printOpen(stdout, g, cfg)

	deps := find.Deps{
		Coll:     coll,
		Graph:    g,
		Embedder: kbembed.New(cfg.KB.EmbedOptions(), cfg.EmbedFallback(), 0, nil),
		Reranker: kbrerank.New(cfg.KB.RerankOptions()),
	}
	// Числа — как у EntryEval: настройки [kb] и [mix] владельца, без поправок.
	opts := find.Opts{
		Collection: name, TopK: cfg.KB.TopK, MaxPerBook: cfg.KB.MaxPerBook,
		MinCosine: cfg.KB.MinCosine, SemanticWeight: cfg.KB.SemanticWeight,
		Semantic: cfg.KB.Semantic, Rerank: deps.Reranker != nil,
		Entities: cfg.Mix.Entities, Neighbors: cfg.Mix.Neighbors,
		Rank: NeighborRank(cfg), QueryTimeout: cfg.KB.QueryTimeoutDuration(),
		RerankOpts: kb.RerankOpts{Candidates: cfg.KB.RerankCandidates, Snippet: cfg.KB.RerankSnippet},
	}

	var out *json.Encoder
	if o.Out != "" {
		f, err := os.Create(o.Out)
		if err != nil {
			return err
		}
		defer f.Close()
		out = json.NewEncoder(f)
	}

	fmt.Fprintf(stdout, "\nзамер слияния книги + граф (П7): %s, коллекция %s, вопросов %d (без chunk_id пропущено %d), TopK %d\n",
		setPath, name, len(cases), skipped, opts.TopK)

	type armStat struct {
		hit, hit1, hit3 int
		rr              float64
		fromG           int
	}
	stats := make([]armStat, len(o.Ks))
	// Попарно с правилом 0: b — нашло только оно, c — только правило k.
	only0, onlyK := make([]int, len(o.Ks)), make([]int, len(o.Ks))
	var inBooks, inGraph, graphOnly, failed, mismatch int
	t0 := time.Now()
	for i, c := range cases {
		if i > 0 && i%25 == 0 {
			fmt.Fprintf(os.Stderr, "  %d из %d, %s\n", i, len(cases), time.Since(t0).Round(time.Second))
		}
		res, err := find.Search(context.Background(), deps, c.Query, opts)
		if err != nil {
			failed++
			continue
		}
		row := mergeRow{N: i + 1, Query: c.Query, Gold: c.ChunkID,
			Places: map[string]int{}, FromG: map[string]int{}}
		for j, h := range res.Hits {
			if h.ID == c.ChunkID {
				row.InBooks = j + 1
				break
			}
		}
		for j, k := range res.Evidence {
			if fmt.Sprintf("%s/%d#%d", name, k.Doc, k.Ord) == c.ChunkID {
				row.InGraph = j + 1
				break
			}
		}
		if row.InBooks > 0 {
			inBooks++
		}
		if row.InGraph > 0 {
			inGraph++
			if row.InBooks == 0 {
				graphOnly++
			}
		}
		found := make([]bool, len(o.Ks))
		for a, k := range o.Ks {
			ro := opts
			ro.MergeRRFK = k
			ex := find.Remerge(coll, res, ro)
			if k == 0 && !sameIDs(ex, res.Excerpts) {
				mismatch++
			}
			place, fromG := 0, 0
			for j, e := range ex {
				if e.Graph {
					fromG++
				}
				if place == 0 && e.ID == c.ChunkID {
					place = j + 1
				}
			}
			arm := mergeArm(k)
			row.Places[arm], row.FromG[arm] = place, fromG
			st := &stats[a]
			st.fromG += fromG
			if place > 0 {
				found[a] = true
				st.hit++
				st.rr += 1 / float64(place)
				if place == 1 {
					st.hit1++
				}
				if place <= 3 {
					st.hit3++
				}
			}
		}
		for a := 1; a < len(o.Ks); a++ {
			switch {
			case found[0] && !found[a]:
				only0[a]++
			case !found[0] && found[a]:
				onlyK[a]++
			}
		}
		if out != nil {
			if err := out.Encode(row); err != nil {
				return err
			}
		}
	}
	n := len(cases) - failed
	fmt.Fprintf(stdout, "прошло вопросов %d, сбоев поиска %d, за %s\n", n, failed, time.Since(t0).Round(time.Second))
	fmt.Fprintf(stdout, "проверка прибора: пересливание правила 0 разошлось с выдачей Search в %d из %d\n", mismatch, n)
	fmt.Fprintf(stdout, "эталон в списке книг %d, среди подтверждений графа %d, ТОЛЬКО в графе %d\n\n", inBooks, inGraph, graphOnly)
	fmt.Fprintf(stdout, "%-8s %8s %8s %8s %8s %12s %18s\n", "правило", "hit@K", "hit@1", "hit@3", "MRR", "граф в выд.", "против хвоста b/c p")
	for a, k := range o.Ks {
		st := stats[a]
		cmp := ""
		if a > 0 {
			cmp = fmt.Sprintf("%d/%d %.3f", only0[a], onlyK[a], mcnemarP(only0[a], onlyK[a]))
		}
		fmt.Fprintf(stdout, "%-8s %8d %8d %8d %8.3f %12.2f %18s\n", mergeArm(k), st.hit, st.hit1, st.hit3,
			st.rr/float64(max(n, 1)), float64(st.fromG)/float64(max(n, 1)), cmp)
	}
	fmt.Fprintf(stdout, "\nhit@K — эталон в слитой выдаче из TopK; «граф в выд.» — среднее число выдержек графа;\n"+
		"b — нашло только правило «хвост», c — только это правило; p — точный двусторонний Мак-Немар.\n")
	return nil
}

func sameIDs(a, b []find.Excerpt) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

// mcnemarP — точный двусторонний тест Мак-Немара по несогласным парам b и c:
// биномиальный с p = 0,5, удвоенный хвост, не больше 1.
func mcnemarP(b, c int) float64 {
	n := b + c
	if n == 0 {
		return 1
	}
	lo := min(b, c)
	sum := 0.0
	for i := 0; i <= lo; i++ {
		lg, _ := math.Lgamma(float64(n + 1))
		li, _ := math.Lgamma(float64(i + 1))
		lr, _ := math.Lgamma(float64(n - i + 1))
		sum += math.Exp(lg - li - lr - float64(n)*math.Ln2)
	}
	return math.Min(1, 2*sum)
}
