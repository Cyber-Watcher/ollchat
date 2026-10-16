package maint

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"

	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
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

// prfDocs — сколько первых кусков слияния дают слова для дописывания
// (--kb-eval-prf): «топ-3» из пункта О8 этапа 112.
const prfDocs = 3

// evalFile — разбор файла набора.
type evalFile struct {
	Case []kb.EvalCase `toml:"case"`
}

// expand > 0 — расширять вопрос именами понятий графа (как делают инструмент
// kb_search и подмес, `find.Expand` с пределом 3), чтобы замерить, помогает
// ли граф простому поиску факта или мешает (этап 105, Б1: «On simple fact
// lookup, plain vector RAG edges out the best graph method»). 0 — как у
// `/search`: без расширения.
//
// out — файл, куда пишется по строке JSON на каждый вопрос и режим
// (`kb.EvalRow` с полем mode); пусто — не писать. Таблицы печатают итог,
// а порог воздержания и проверку ответа реранкером подбирают по строкам
// (этап 112, О6, О10, О11, Л3).
//
// books > 0 — ступень «в какой книге искать» (этап 112, У2): книги
// упорядочиваются по близости вопроса к вектору книги (среднему векторов её
// кусков), куски ищутся только в первых books из них, а в отчёт идёт, на каком
// месте стоит книга эталонного куска. 0 — как в работе: по всем книгам.
//
// prf > 0 — псевдо-релевантная обратная связь (этап 112, О8): первые prfDocs
// куска слияния (слова и векторы, без реранкера) дают prf самых весомых по
// TF-IDF слов, и они дописываются к СЛОВЕСНОЙ части вопроса; вектор вопроса
// и реранкер видят вопрос как есть. 0 — без дописывания.
//
// translate — файл-кэш английских переводов вопросов (kbtranslate.go): перевод
// дописывается к словесной ветке, недостающие переводит модель
// graph.summary_model. Пусто — без перевода.
func Eval(stdout io.Writer, cfg *config.Config, name, path string, topK int, weight, rrfk, tableBoost float64,
	only string, rerank bool, candidates int, snippet bool, expand, wide int, dedupe float64,
	keepAdjacent bool, out string, books, prf int, translate string, translateWorkers int, part string,
	window, windowSelf bool) error {
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
	total := len(f.Case)
	if part != "" {
		if f.Case, err = evalPart(f.Case, part); err != nil {
			return err
		}
		if len(f.Case) == 0 {
			return fmt.Errorf("в части %s набора %s нет ни одного вопроса", part, path)
		}
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

	fallback := cfg.EmbedFallback()
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
	opt := kb.EvalOpts{K: topK, SemanticWeight: weight, TableBoost: tableBoost, WideK: wide,
		KeepAdjacent: keepAdjacent}
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
	var g *graph.Graph
	if expand > 0 {
		if g, err = graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules()); err != nil {
			return fmt.Errorf("расширение вопроса графом: %w", err)
		}
		defer g.Close()
	}
	// Переводы вопросов: из кэша, недостающие — моделью (карта занята только
	// на время перевода).
	var trans map[string]string
	if translate != "" {
		if trans, err = loadTranslations(translate); err != nil {
			return err
		}
		if err := translateMissing(ctx, cfg, translate, f.Case, trans, translateWorkers); err != nil {
			return err
		}
	}
	// Векторы книг считаются один раз на прогон: это проход по всем векторам
	// кусков коллекции.
	var cents map[uint32][]float32
	if books > 0 {
		fmt.Fprintln(os.Stderr, "считаю векторы книг (среднее векторов кусков)…")
		if cents = coll.BookCentroids(); len(cents) == 0 {
			return fmt.Errorf("векторы книг: у коллекции %s нет векторов кусков", name)
		}
	}
	// Второй векторный вид — окно из трёх кусков (С9): строится один раз на прогон.
	var wv *kb.WindowView
	if window || windowSelf {
		fmt.Fprintln(os.Stderr, "строю второй вид: окна из трёх соседних кусков (сумма векторов)…")
		if wv = coll.NewWindowView(windowSelf); wv == nil {
			return fmt.Errorf("второй вид: у коллекции %s нет векторов кусков", name)
		}
	}
	// prfShown — сколько примеров дописанных слов уже показано: первые пять
	// идут в stderr, чтобы глазами видеть, ЧТО именно дописывается.
	prfShown := 0
	// Замер идёт тем же ядром, что и работа: find.Books (этап 91, R2.9).
	// Числа режима (слова / смысл / слияние) приходят из kb.searchOptsFor.
	opt.Search = func(ctx context.Context, query string, so kb.SearchOpts, want int) ([]kb.Result, error) {
		// Книги выбираются по исходному вопросу, до расширения графом.
		var docs []uint32
		if books > 0 {
			d, err := coll.BooksByCentroid(ctx, emb, query, cents, books)
			if err != nil {
				return nil, fmt.Errorf("векторы книг: %w", err)
			}
			docs = d
		}
		extra := ""
		if prf > 0 {
			first, _, err := find.Books(ctx, find.Deps{Coll: coll, Embedder: emb}, query, query, find.Opts{
				Mode: "eval", Collection: name, TopK: prfDocs, Semantic: true,
				QueryTimeout: kb.DefaultQueryTimeout, Docs: docs,
			})
			if err != nil {
				return nil, fmt.Errorf("слова из первых кусков: %w", err)
			}
			texts := make([]string, 0, len(first))
			for _, r := range first {
				texts = append(texts, r.Text)
			}
			if terms := coll.PRFTerms(query, texts, prf); len(terms) > 0 {
				extra = strings.Join(terms, " ")
				if prfShown < 5 {
					prfShown++
					fmt.Fprintf(os.Stderr, "  слова из первых кусков: %s → %s\n", query, strings.Join(terms, " "))
				}
			}
		}
		if en := trans[query]; en != "" {
			extra = strings.TrimSpace(extra + " " + en)
		}
		if g != nil {
			// Тот же вход, что у подмеса: понятия с двумя и более упоминаниями.
			res := g.Search(query, graph.SearchOpts{TopEntities: cfg.Mix.Entities, MinMentions: 2})
			query = find.Expand(query, res.Entities, find.Opts{ExpandLimit: expand})
		}
		fo := find.Opts{
			Mode: "eval", Collection: name, TopK: want, MaxPerBook: so.MaxPerDoc,
			Semantic: so.Semantic, SemanticOnly: so.SemanticOnly,
			TableBoost: so.TableBoost, RRFK: so.RRFK, SemanticWeight: so.SemanticWeight,
			QueryTimeout: kb.DefaultQueryTimeout,
			DedupeCosine: dedupe,
			KeepAdjacent: keepAdjacent,
			Rerank:       opt.Rerank != nil, RerankOpts: opt.RerankOpts,
			Docs: docs, WordsExtra: extra,
		}
		hits, _, err := find.Books(ctx, find.Deps{Coll: coll, Embedder: emb, Reranker: opt.Rerank}, query, query, fo)
		if err != nil || wv == nil {
			return hits, err
		}
		// Два вида одного документа сливаются по RRF (С9): обычная выдача и окна.
		wh, err := wv.Search(ctx, emb, query, want)
		if err != nil {
			return nil, fmt.Errorf("второй вид: %w", err)
		}
		return kb.FuseRRF(hits, wh, 0, want), nil
	}

	fmt.Fprintf(stdout, "коллекция %s, вопросов %d, смотрим первые %d", name, len(f.Case), topK)
	if part != "" {
		fmt.Fprintf(stdout, ", часть %s: %d из %d вопросов (по хешу текста вопроса)", part, len(f.Case), total)
	}
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
	if expand > 0 {
		fmt.Fprintf(stdout, ", вопрос расширен именами понятий графа (до %d)", expand)
	}
	if dedupe > 0 {
		fmt.Fprintf(stdout, ", повторы выдачи отброшены по близости ≥ %.2f", dedupe)
	}
	if keepAdjacent {
		fmt.Fprintf(stdout, ", СОСЕДНИЕ КУСКИ НЕ ВЫБРАСЫВАЮТСЯ (замер З5)")
	}
	if books > 0 {
		fmt.Fprintf(stdout, ", куски ищутся только в %d книгах, ближайших по вектору книги (из %d)", books, len(cents))
	}
	switch {
	case windowSelf:
		fmt.Fprintf(stdout, ", КОНТРОЛЬ С9: второй список — векторы самих кусков, слит с выдачей по RRF")
	case window:
		fmt.Fprintf(stdout, ", ВТОРОЙ ВИД: окна из трёх соседних кусков слиты с выдачей по RRF (С9)")
	}
	if prf > 0 {
		fmt.Fprintf(stdout, ", к словам дописаны %d слов из первых %d кусков слияния", prf, prfDocs)
	}
	if translate != "" {
		n := 0
		for _, cs := range f.Case {
			if trans[cs.Query] != "" {
				n++
			}
		}
		fmt.Fprintf(stdout, ", к словам дописан английский перевод вопроса (есть у %d из %d)", n, len(f.Case))
	}
	fmt.Fprintln(stdout)
	// Паспорт прогона (этап 112, У3): без него числа двух прогонов не сравнить —
	// между ними могли долить книги, пересчитать векторы или сменить разбор.
	fmt.Fprintln(stdout, evalPassport(coll.Stats()))
	if emb == nil {
		fmt.Fprintln(stdout, "внимание: kb.embed_model не задан — смысловые режимы мерить нечем")
	}
	fmt.Fprintf(stdout, "\n%-10s %10s %8s %8s %10s %8s %8s %10s\n",
		"режим", "recall@K", "MRR", "nDCG", "ср. место", "мимо", "выпало", "молчит")
	fmt.Fprintln(stdout, "  "+dashes(76))
	// «Молчит» — рядом с recall, а не отдельной таблицей в конце (этап 112, Ч20):
	// режим, который находит больше, может чаще отвечать там, где искать нечего
	// («a system that retrieves something answers», Mondal, `566#982–983`).
	fmt.Fprintf(stdout, "  (молчит — при рабочем пороге %s: всего / из них зря, нужный кусок был)\n",
		workAbstainName(cfg.KB.AbstainScore, cfg.KB.AbstainGap, opt.Rerank != nil))
	// «Выпало» — из «мимо»: нужный кусок достижим при щедром бюджете, но места
	// ему не хватило. Без ключа колонка пуста, и это сказано прямо: молчащий
	// столбец читался бы как «таких вопросов нет» (этап 105, Ж2).
	if wide <= 0 {
		fmt.Fprintf(stdout, "  (колонка «выпало» не мерена: --kb-eval-wide N — щедрый бюджет для промахнувшихся вопросов)\n")
	}

	// При подборе веса словесный и смысловой режимы не меняются вовсе, а стоят
	// двух третей времени прогона. Замер 26.08.2026: восемь прогонов подбора
	// на 457 вопросах заняли около получаса, из них две трети — пересчёт
	// одного и того же.
	modes := kb.EvalModes
	if only != "" {
		modes = []kb.EvalMode{kb.EvalMode(only)}
	}

	var rows *os.File
	if out != "" {
		if rows, err = os.Create(out); err != nil {
			return fmt.Errorf("строки замера: %w", err)
		}
		defer rows.Close()
	}

	var missedBy = map[kb.EvalMode][]string{}
	var droppedBy = map[kb.EvalMode][]string{}
	var fusionGaps []kb.GapPoint
	var gapsMode kb.EvalMode
	for _, mode := range modes {
		rep, err := coll.Eval(ctx, f.Case, mode, opt, emb)
		if err != nil {
			return err
		}
		if rows != nil {
			if err := writeEvalRows(rows, mode, rep.Rows); err != nil {
				return fmt.Errorf("строки замера %s: %w", out, err)
			}
		}
		missedBy[mode] = rep.Missed
		// Таблица — по слиянию (так ищет kb_search), а при явном --kb-eval-only
		// по выбранному режиму: шкалы у них разные, и сравнить их полезно.
		if mode == kb.EvalFusion || only != "" {
			fusionGaps, gapsMode = rep.Gaps, mode
		}
		dropped := "—"
		if wide > 0 {
			dropped = fmt.Sprintf("%d", len(rep.Dropped))
		}
		fmt.Fprintf(stdout, "%-10s %10.3f %8.3f %8.3f %10.1f %8d %8s %10s\n",
			string(mode), rep.Recall, rep.MRR, rep.NDCG, rep.AvgRank, len(rep.Missed), dropped,
			workAbstain(rep.Gaps, cfg.KB.AbstainScore, cfg.KB.AbstainGap, opt.Rerank != nil))
		// Вторая мера — «та же страница» (этап 105, З4): попаданием считается
		// и кусок-сосед, накрывающий страницу эталона. Печатается РЯДОМ
		// со строгой, чтобы прежние числа остались сравнимыми.
		if rep.RecallNear > rep.Recall {
			fmt.Fprintf(stdout, "%-10s %10.3f %8.3f %8.3f %10.1f %8d %8s  ← попаданием считается и кусок той же страницы\n",
				"  та же стр.", rep.RecallNear, rep.MRRNear, rep.NDCGNear, rep.AvgRankNear,
				len(f.Case)-int(rep.RecallNear*float64(len(f.Case))+0.5), "—")
		}
		// Разбор промахов: сколько из них промахи ПРИБОРА, а не поиска.
		// Печатается по тому же режиму, что и таблица, и только когда промахи
		// есть, — иначе строка была бы шумом.
		if len(rep.Missed) > 0 && rep.NearBook > 0 {
			near := append([]int(nil), rep.NearDist...)
			sort.Ints(near)
			fmt.Fprintf(stdout, "  · из %d промахов та же книга нашлась у %d (%.0f%%), из них кусок рядом (±2) у %d (%.0f%%); медиана расстояния %d кусков\n",
				len(rep.Missed), rep.NearBook, 100*float64(rep.NearBook)/float64(len(rep.Missed)),
				rep.NearChunk, 100*float64(rep.NearChunk)/float64(len(rep.Missed)),
				near[len(near)/2])
		}
		if wide > 0 && len(rep.Dropped) > 0 {
			fmt.Fprintf(stdout, "  · из них место в щедром списке не ниже %d: %d (срезал предел на книгу max_per_book=%d), ниже: %d (срезал top_k=%d)\n",
				topK, rep.DroppedInK, cfg.KB.MaxPerBook, len(rep.Dropped)-rep.DroppedInK, topK)
			droppedBy[mode] = rep.Dropped
		}
	}

	if books > 0 {
		if err := printBookRanks(ctx, stdout, coll, emb, f.Case, cents); err != nil {
			return err
		}
	}

	printRagasNames(stdout)

	// Порог воздержания (этап 91, R2.11): ниже какого разрыва первого
	// и второго места выдача — шум. Таблица по режиму слияния, потому что
	// так ищет kb_search; с реранкером шкала другая, и порог у него свой.
	if len(fusionGaps) > 0 {
		printAbstainTable(stdout, fusionGaps, gapsMode, opt.Rerank != nil)
	}

	// Вопросы, на которых промахнулось слияние, — это и есть список работ.
	// Их полезно видеть глазами, а не только счётчиком.
	if miss := missedBy[kb.EvalFusion]; len(miss) > 0 {
		dropped := map[string]bool{}
		for _, q := range droppedBy[kb.EvalFusion] {
			dropped[q] = true
		}
		fmt.Fprintf(stdout, "\nслияние не нашло (%d):\n", len(miss))
		for _, q := range miss {
			if dropped[q] {
				// Помета важнее вежливости: такой вопрос лечится бюджетом
				// выдачи, а не поиском, и в списке работ ему не место.
				fmt.Fprintln(stdout, "  · [выпало по бюджету]", q)
				continue
			}
			fmt.Fprintln(stdout, "  ·", q)
		}
	}
	return nil
}

// printBookRanks печатает, на каком месте среди книг стоит книга эталонного
// куска, если книги упорядочить по близости вопроса к вектору книги (этап 112,
// У2). Это потолок ступени «в какой книге искать»: книга эталона за пределами
// первых N — кусок не найдётся, как бы хорошо ни искали внутри.
func printBookRanks(ctx context.Context, w io.Writer, coll *kb.Collection, emb kb.Embedder,
	cases []kb.EvalCase, cents map[uint32][]float32) error {
	cuts := []int{1, 3, 10, 20, 50}
	within := make([]int, len(cuts))
	var places []int
	for _, cs := range cases {
		gold, ok := kb.DocOfChunkID(cs.ChunkID)
		if !ok {
			continue
		}
		order, err := coll.BooksByCentroid(ctx, emb, cs.Query, cents, 0)
		if err != nil {
			return fmt.Errorf("векторы книг: %w", err)
		}
		// Пустой порядок — нет эмбеддера или векторов: место «за последним»
		// при нуле книг стало бы первым, и замер засчитал бы всё.
		if len(order) == 0 {
			return fmt.Errorf("векторы книг: порядок книг пуст — нет эмбеддера (kb.embed_model) или векторов кусков")
		}
		// Книги эталона нет среди книг с вектором — место за последним.
		place := len(order) + 1
		for i, d := range order {
			if d == gold {
				place = i + 1
				break
			}
		}
		places = append(places, place)
		for i, k := range cuts {
			if place <= k {
				within[i]++
			}
		}
	}
	if len(places) == 0 {
		fmt.Fprintln(w, "\nкнига эталона: в наборе нет chunk_id — мерить нечего")
		return nil
	}
	sort.Ints(places)
	fmt.Fprintf(w, "\nкнига эталона по вектору книги (книг с вектором %d, вопросов с эталоном %d):\n",
		len(cents), len(places))
	for i, k := range cuts {
		fmt.Fprintf(w, "  в первых %2d книгах: %4d (%.1f%%)\n", k, within[i],
			100*float64(within[i])/float64(len(places)))
	}
	fmt.Fprintf(w, "  медиана места %d\n", places[len(places)/2])
	return nil
}

// writeEvalRows дописывает строки одного режима: поля kb.EvalRow плюс mode.
func writeEvalRows(w io.Writer, mode kb.EvalMode, rows []kb.EvalRow) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		line := struct {
			Mode kb.EvalMode `json:"mode"`
			kb.EvalRow
		}{mode, r}
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	return nil
}

// printRagasNames — как наши колонки называются в общепринятых терминах.
//
// **Зачем.** Книги делят оценку RAG на четыре метрики (Ragas): faithfulness,
// answer relevancy, context precision, context recall («Advanced RAG», гл. 9.5,
// стр. 432–433; «LLM Engineer's Handbook», стр. 891). Наш замер закрывает две
// из них, но под своими именами — и со стороны это читается как «у них меряют
// одно, у нас другое». Одна строка снимает вопрос и заодно честно говорит,
// чего здесь нет и почему.
func printRagasNames(stdout io.Writer) {
	fmt.Fprintln(stdout, "\nТо же в общепринятых названиях (Ragas):")
	fmt.Fprintln(stdout, "  recall@K   = context recall — нашлось ли нужное место среди первых K")
	fmt.Fprintln(stdout, "  MRR, nDCG ≈ context precision — насколько высоко оно стоит,")
	fmt.Fprintln(stdout, "               то есть сколько лишнего модель прочтёт перед ним")
	fmt.Fprintln(stdout, "  faithfulness и answer relevancy здесь не считаются: они про ОТВЕТ")
	fmt.Fprintln(stdout, "               модели, а этот замер идёт без генерации вовсе")
	fmt.Fprintln(stdout, "               (для них нужен набор с полем must — что обязано прозвучать в ответе)")
}

func dashes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}

// devShare — доля набора на подбор (часть dev); остальное — отложенная часть.
const devShare = 70

// evalPart оставляет одну часть набора: dev — для подбора порогов и весов,
// holdout — для вывода перед выкладкой (этап 112, Л2, Ч12: «преимущество
// гибрида на вопросах, по которым его строили, сократилось на отложенных»,
// Venn, 2026, стр. 223). Часть решает хеш FNV-1a текста вопроса, а не место
// в файле: дописанные вопросы и перестановка не перекладывают старые.
func evalPart(cases []kb.EvalCase, part string) ([]kb.EvalCase, error) {
	if part != "dev" && part != "holdout" {
		return nil, fmt.Errorf("часть набора — dev или holdout, а не %q", part)
	}
	var out []kb.EvalCase
	for _, cs := range cases {
		h := fnv.New32a()
		h.Write([]byte(cs.Query))
		if (h.Sum32()%100 < devShare) == (part == "dev") {
			out = append(out, cs)
		}
	}
	return out, nil
}

// evalPassport — строка паспорта коллекции в шапке --kb-eval: книги, куски,
// векторы с моделью и размерностью, версия разбора.
func evalPassport(st kb.Stats) string {
	s := fmt.Sprintf("паспорт: книг %d (с текстом %d), кусков %d, векторов %d", st.Books, st.Indexed, st.Chunks, st.Vectors)
	if st.VecModel != "" {
		s += fmt.Sprintf(" (%s, размерность %d)", st.VecModel, st.VecDim)
	}
	s += ", разбор " + st.Analyzer
	if st.Stale {
		s += " — УСТАРЕЛ, нужен --kb-reanalyze"
	}
	return s
}

// workAbstain — сколько вопросов промолчали бы при рабочем пороге и сколько из
// них зря (нужный кусок был): «12/2». Порог тот же, что у kb_search: с реранкером —
// абсолютная оценка первого места (kb.abstain_score), без него — разрыв первого
// и второго места (kb.abstain_gap). «—» — воздержание этим путём выключено.
// Счёт — теми же kb.AbstainTable и kb.AbstainTableTop1, что и таблица в конце.
func workAbstain(points []kb.GapPoint, score *float64, gap float64, rerank bool) string {
	var rows []kb.AbstainRow
	switch {
	case rerank && score != nil:
		rows = kb.AbstainTableTop1(points, []float64{*score})
	case gap > 0:
		rows = kb.AbstainTable(points, []float64{gap})
	default:
		return "—"
	}
	if len(rows) == 0 {
		return "—"
	}
	return fmt.Sprintf("%d/%d", rows[0].Silent, rows[0].SilentWrong)
}

// workAbstainName — какой порог стоит за колонкой «молчит».
func workAbstainName(score *float64, gap float64, rerank bool) string {
	switch {
	case rerank && score != nil:
		return fmt.Sprintf("kb.abstain_score = %.2f по оценке реранкера", *score)
	case gap > 0:
		return fmt.Sprintf("kb.abstain_gap = %.2f по разрыву мест", gap)
	}
	return "нет: воздержание выключено (без реранкера kb.abstain_gap = 0)"
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
