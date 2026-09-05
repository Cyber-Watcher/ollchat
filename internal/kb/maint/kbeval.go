package maint

import (
	"context"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
)

// Eval меряет качество поиска по замерному набору.
//
// Три режима подряд — только слова, только смыслы, слияние — чтобы видеть
// не «хорошо или плохо», а что именно даёт каждый поиск и стоит ли слияние
// своей сложности. Числа печатаются рядом, потому что порознь они ничего
// не значат: recall@10 в 0.8 — это много или мало, известно только в сравнении
// с соседней строкой.

// evalFile — разбор файла набора.
type evalFile struct {
	Case []kb.EvalCase `toml:"case"`
}

func Eval(stdout io.Writer, cfg *config.Config, name, path string, topK int, weight, rrfk, tableBoost float64,
	only string, rerank bool, candidates int, snippet bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("набор %s: %w", path, err)
	}
	var f evalFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("набор %s: %w", path, err)
	}
	if len(f.Case) == 0 {
		return fmt.Errorf("в наборе %s нет ни одного вопроса", path)
	}

	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	emb := kbembed.New(cfg.KB.EmbedOptions(), fallback, 5*time.Minute, nil)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if topK <= 0 {
		topK = 10
	}
	// Вес смыслового списка и постоянная слияния — единственные ручки, которыми
	// слияние настраивается. Подбирать их на глаз нельзя: замер 26.08.2026
	// показал, что при равных весах слияние находит больше, чем один смысловой
	// поиск, но ставит найденное ниже. Флаги нужны, чтобы перебрать значения
	// и выбрать по числам, а не по ощущению.
	if tableBoost <= 0 {
		tableBoost = cfg.KB.TableBoost // «как в работе» — то, что стоит в настройках
	}
	opt := kb.EvalOpts{K: topK, SemanticWeight: weight, TableBoost: tableBoost}
	if rerank {
		rr := kbrerank.New(cfg.KB.RerankOptions())
		if rr == nil {
			return fmt.Errorf("переранжирование не настроено: задайте kb.rerank_url в %s", cfg.Path)
		}
		if err := rr.Check(ctx); err != nil {
			return err
		}
		opt.Rerank = rr
		opt.RerankOpts = kb.RerankOpts{Candidates: candidates, Snippet: snippet}
	}
	if rrfk > 0 {
		opt.RRFK = rrfk
	}
	// Замер идёт тем же ядром, что и работа: find.Books (этап 91, R2.9).
	// Числа режима (слова / смысл / слияние) приходят из kb.searchOptsFor.
	opt.Search = func(ctx context.Context, query string, so kb.SearchOpts, want int) ([]kb.Result, error) {
		fo := find.Opts{
			Mode: "eval", Collection: name, TopK: want, MaxPerBook: so.MaxPerDoc,
			Semantic: so.Semantic, SemanticOnly: so.SemanticOnly,
			TableBoost: so.TableBoost, RRFK: so.RRFK, SemanticWeight: so.SemanticWeight,
			QueryTimeout: kb.DefaultQueryTimeout,
			Rerank:       opt.Rerank != nil, RerankOpts: opt.RerankOpts,
		}
		hits, _, err := find.Books(ctx, find.Deps{Coll: coll, Embedder: emb, Reranker: opt.Rerank}, query, query, fo)
		return hits, err
	}

	fmt.Fprintf(stdout, "коллекция %s, вопросов %d, смотрим первые %d", name, len(f.Case), topK)
	if opt.Rerank != nil {
		kind := "куски целиком"
		if snippet {
			kind = "выдержки"
		}
		fmt.Fprintf(stdout, ", переранжирование %s: кандидатов %d, %s",
			opt.Rerank.Model(), opt.RerankOpts.Candidates, kind)
	}
	if weight > 0 {
		fmt.Fprintf(stdout, ", вес смыслового списка %.2f", weight)
	}
	if rrfk > 0 {
		fmt.Fprintf(stdout, ", k слияния %.0f", rrfk)
	}
	fmt.Fprintln(stdout)
	if emb == nil {
		fmt.Fprintln(stdout, "внимание: kb.embed_model не задан — смысловые режимы мерить нечем")
	}
	fmt.Fprintf(stdout, "\n%-10s %10s %8s %8s %10s %8s\n", "режим", "recall@K", "MRR", "nDCG", "ср. место", "мимо")
	fmt.Fprintln(stdout, "  "+dashes(56))

	// При подборе веса словесный и смысловой режимы не меняются вовсе, а стоят
	// двух третей времени прогона. Замер 26.08.2026: восемь прогонов подбора
	// на 457 вопросах заняли около получаса, из них две трети — пересчёт
	// одного и того же.
	modes := kb.EvalModes
	if only != "" {
		modes = []kb.EvalMode{kb.EvalMode(only)}
	}

	var missedBy = map[kb.EvalMode][]string{}
	var fusionGaps []kb.GapPoint
	var gapsMode kb.EvalMode
	for _, mode := range modes {
		rep, err := coll.Eval(ctx, f.Case, mode, opt, emb)
		if err != nil {
			return err
		}
		missedBy[mode] = rep.Missed
		// Таблица — по слиянию (так ищет kb_search), а при явном --kb-eval-only
		// по выбранному режиму: шкалы у них разные, и сравнить их полезно.
		if mode == kb.EvalFusion || only != "" {
			fusionGaps, gapsMode = rep.Gaps, mode
		}
		fmt.Fprintf(stdout, "%-10s %10.3f %8.3f %8.3f %10.1f %8d\n",
			string(mode), rep.Recall, rep.MRR, rep.NDCG, rep.AvgRank, len(rep.Missed))
	}

	// Порог воздержания (этап 91, R2.11): ниже какого разрыва первого
	// и второго места выдача — шум. Таблица по режиму слияния, потому что
	// так ищет kb_search; с реранкером шкала другая, и порог у него свой.
	if len(fusionGaps) > 0 {
		printAbstainTable(stdout, fusionGaps, gapsMode, opt.Rerank != nil)
	}

	// Вопросы, на которых промахнулось слияние, — это и есть список работ.
	// Их полезно видеть глазами, а не только счётчиком.
	if miss := missedBy[kb.EvalFusion]; len(miss) > 0 {
		fmt.Fprintf(stdout, "\nслияние не нашло (%d):\n", len(miss))
		for _, q := range miss {
			fmt.Fprintln(stdout, "  ·", q)
		}
	}
	return nil
}

func dashes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}

// printAbstainTable печатает, что дал бы каждый порог разрыва: сколько
// вопросов промолчали бы, из них честно (нужного куска не было) и зря (был).
//
// Порог выбирают по паре чисел: молчать как можно чаще там, где искать
// нечего, и как можно реже там, где нашлось. Выбранное значение — в
// kb.abstain_gap; 0 выключает воздержание.
func printAbstainTable(w io.Writer, points []kb.GapPoint, mode kb.EvalMode, rerank bool) {
	thresholds := []float64{0.01, 0.02, 0.03, 0.05, 0.08, 0.1, 0.15, 0.2, 0.3, 0.5}
	rows := kb.AbstainTable(points, thresholds)
	hits, miss := 0, 0
	for _, p := range points {
		if p.Hit {
			hits++
		} else {
			miss++
		}
	}
	scale := "режим " + string(mode)
	if rerank {
		scale = "оценки реранкера"
	}
	fmt.Fprintf(w, "\nвоздержание по разрыву первого и второго места (%s): нашлось %d, мимо %d\n", scale, hits, miss)
	fmt.Fprintf(w, "%-8s %10s %14s %12s\n", "порог", "молчит", "честно (мимо)", "зря (нашлось)")
	fmt.Fprintln(w, "  "+dashes(46))
	for _, r := range rows {
		right, wrong := 0.0, 0.0
		if miss > 0 {
			right = 100 * float64(r.SilentRight) / float64(miss)
		}
		if hits > 0 {
			wrong = 100 * float64(r.SilentWrong) / float64(hits)
		}
		fmt.Fprintf(w, "%-8.2f %10d %7d %5.1f%% %6d %4.1f%%\n", r.Threshold, r.Silent, r.SilentRight, right, r.SilentWrong, wrong)
	}
	fmt.Fprintln(w, "выбранный порог — в kb.abstain_gap (0 — воздержания нет)")

	// Абсолютный порог по оценке первого места — только у реранкера: его шкала
	// сопоставима между запросами (по делу около +1, не по делу около −11),
	// а оценки слияния по рангам — нет (этап 89, шаг 4).
	if !rerank {
		return
	}
	abs := []float64{-10, -8, -6, -5, -4, -3, -2, -1, -0.5, 0, 0.5, 1, 2}
	fmt.Fprintf(w, "\nвоздержание по абсолютной оценке первого места (реранкер): нашлось %d, мимо %d\n", hits, miss)
	fmt.Fprintf(w, "%-8s %10s %14s %12s\n", "порог", "молчит", "честно (мимо)", "зря (нашлось)")
	fmt.Fprintln(w, "  "+dashes(46))
	for _, r := range kb.AbstainTableTop1(points, abs) {
		right, wrong := 0.0, 0.0
		if miss > 0 {
			right = 100 * float64(r.SilentRight) / float64(miss)
		}
		if hits > 0 {
			wrong = 100 * float64(r.SilentWrong) / float64(hits)
		}
		fmt.Fprintf(w, "%-8.1f %10d %7d %5.1f%% %6d %4.1f%%\n", r.Threshold, r.Silent, r.SilentRight, right, r.SilentWrong, wrong)
	}
}
