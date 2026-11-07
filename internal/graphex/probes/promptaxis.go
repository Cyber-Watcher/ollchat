// promptaxis.go — ось prompt режима `-only stability` (07.11.2026, этап 126, Я1):
// как извлечение меняется от замены системного промпта. Обычные куски прозы
// (отбор — как у оси temp: без кода, оглавлений и литературы, от 600 знаков,
// `-n` штук равномерно по обходу) идут модели четыре раза: старым промптом
// graph.SystemPrompt дважды и промптом-кандидатом дважды, при рабочей
// температуре. Ничего не пишет ни в граф, ни в коллекцию; карта нужна.
//
// **Чем отличается от осей temp и textfix.** Запросы идут параллельно через пул
// askpool.go (его же гоняет `-only direction`), а не по одному; меняется не
// температура и не текст куска, а промпт; сверяются не только имена понятий, но
// и связи — по типам и по паре прогонов. Файл отдельный из-за размера
// stability.go; диспетчер оси — runStability там же.
//
// **Типы связей.** Восемь корзин: семь названий graph.RelName и «не назван/
// неизвестный». Тип сперва разбирается RelTypeStrict: названный идёт в свою
// корзину; отказ у него и для явного «связано», и для пустого или выдуманного
// типа, поэтому явное «связано» отделяется по написанию, а остальное —
// «не назван/неизвестный» (сборка формата 2 такую связь не пишет).
package probes

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/graphex"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// promptRuns — прогонов на кусок: старый ×2 и кандидат ×2.
const promptRuns = 4

// promptRunNames — имена прогонов в итоге и в TSV; порядок совпадает с индексом.
var promptRunNames = [promptRuns]string{"старый-1", "старый-2", "кандидат-1", "кандидат-2"}

// promptRunPairs — какие прогоны сравниваются попарно.
var promptRunPairs = [][2]int{{0, 1}, {2, 3}, {0, 2}, {1, 3}}

// relBucketNames — корзины связей: семь названий типов, затем «не назван/
// неизвестный». Названия берутся из graph.RelName, а не пишутся здесь.
var relBucketNames = func() []string {
	var names []string
	for t := graph.RelIs; t <= graph.RelRelated; t++ {
		names = append(names, graph.RelName(t))
	}
	return append(names, "не назван/неизвестный")
}()

// relBucket — корзина типа связи, как его написала модель (см. шапку файла).
func relBucket(raw string) int {
	if typ, named := graph.RelTypeStrict(raw); named {
		return int(typ - graph.RelIs)
	}
	if graph.Normalize(raw) == graph.Normalize(graph.RelName(graph.RelRelated)) {
		return int(graph.RelRelated - graph.RelIs)
	}
	return len(relBucketNames) - 1
}

// promptSets — три набора одного ответа для Жаккара.
type promptSets struct {
	Names   map[string]bool // имена понятий (MatchName)
	Pairs   map[string]bool // связь как неупорядоченная пара имён и тип
	Triples map[string]bool // направленная тройка src, тип, dst
}

// setsOf раскладывает ответ в наборы. Разделитель \x1f не встречается в имени.
func setsOf(f graph.Facts) promptSets {
	s := promptSets{Names: nameKeys(f), Pairs: map[string]bool{}, Triples: map[string]bool{}}
	const sep = "\x1f"
	for _, r := range f.Relations {
		a, b := graph.MatchName(r.Src), graph.MatchName(r.Dst)
		typ := strconv.Itoa(relBucket(r.Type))
		s.Triples[a+sep+typ+sep+b] = true
		if a > b {
			a, b = b, a
		}
		s.Pairs[a+sep+b+sep+typ] = true
	}
	return s
}

// promptStat — итог одного прогона по всем кускам.
type promptStat struct {
	Answered, Failed    int   // кусков с ответом и без
	Entities, Relations int   // всего за прогон
	ByBucket            []int // связей по корзинам relBucketNames
}

// tallyPrompt считает итог прогона run.
func tallyPrompt(answers [][]poolAnswer, run int) promptStat {
	st := promptStat{ByBucket: make([]int, len(relBucketNames))}
	for _, ans := range answers {
		if !ans[run].OK {
			st.Failed++
			continue
		}
		st.Answered++
		f := ans[run].Facts
		st.Entities += len(f.Entities)
		st.Relations += len(f.Relations)
		for _, r := range f.Relations {
			st.ByBucket[relBucket(r.Type)]++
		}
	}
	return st
}

// share — доля a от total в процентах; при пустом total — ноль.
func share(a, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(a) / float64(total)
}

// printPromptStat печатает итог прогона, по строке на число.
func printPromptStat(w io.Writer, name string, st promptStat) {
	fmt.Fprintf(w, "прогон %s\n", name)
	fmt.Fprintf(w, "  кусков с ответом: %d\n", st.Answered)
	fmt.Fprintf(w, "  сбоев запроса или разбора: %d\n", st.Failed)
	fmt.Fprintf(w, "  понятий всего: %d\n", st.Entities)
	fmt.Fprintf(w, "  связей всего: %d\n", st.Relations)
	for i, n := range relBucketNames {
		fmt.Fprintf(w, "  связей «%s»: %d (%.1f%%)\n", n, st.ByBucket[i], share(st.ByBucket[i], st.Relations))
	}
	hier := st.ByBucket[relBucket(graph.RelName(graph.RelIs))] + st.ByBucket[relBucket(graph.RelName(graph.RelPart))]
	fmt.Fprintf(w, "  доля «%s»+«%s»: %.1f%%\n", graph.RelName(graph.RelIs), graph.RelName(graph.RelPart), share(hier, st.Relations))
}

// promptJac — средние Жаккары пары прогонов.
type promptJac struct{ Names, Pairs, Triples float64 }

// promptJaccard — средний по кускам Жаккар прогонов a и b по именам понятий, по
// связям (неупорядоченная пара и тип) и по направленным тройкам. Берутся куски,
// где ответили все прогоны: пары прогонов сравнимы на одних и тех же кусках.
// Оба набора пусты — Жаккар 1, как у jaccard. Второй результат — число кусков.
func promptJaccard(answers [][]poolAnswer, a, b int) (promptJac, int) {
	var sum promptJac
	n := 0
	for _, ans := range answers {
		if !allAnswered(ans) {
			continue
		}
		sa, sb := setsOf(ans[a].Facts), setsOf(ans[b].Facts)
		sum.Names += jaccard(sa.Names, sb.Names)
		sum.Pairs += jaccard(sa.Pairs, sb.Pairs)
		sum.Triples += jaccard(sa.Triples, sb.Triples)
		n++
	}
	if n == 0 {
		return promptJac{}, 0
	}
	f := float64(n)
	return promptJac{Names: sum.Names / f, Pairs: sum.Pairs / f, Triples: sum.Triples / f}, n
}

// writePromptTSV пишет построчный итог: кусок, прогон, понятий, связей и связи
// по корзинам. Прогон без ответа — «сбой» в колонке понятий и пустые клетки.
func writePromptTSV(w io.Writer, chunks []poolChunk, answers [][]poolAnswer) {
	fmt.Fprintf(w, "кусок\tпрогон\tпонятий\tсвязей\t%s\n", strings.Join(relBucketNames, "\t"))
	for i, ch := range chunks {
		for r := 0; r < promptRuns; r++ {
			a := answers[i][r]
			if !a.OK {
				// Пустые клетки: «связей» и по одной на корзину.
				fmt.Fprintf(w, "%s\t%s\tсбой%s\n", ch.Key, promptRunNames[r], strings.Repeat("\t", 1+len(relBucketNames)))
				continue
			}
			by := make([]string, len(relBucketNames))
			for j := range by {
				by[j] = "0"
			}
			counts := make([]int, len(relBucketNames))
			for _, rel := range a.Facts.Relations {
				counts[relBucket(rel.Type)]++
			}
			for j, c := range counts {
				by[j] = strconv.Itoa(c)
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\n", ch.Key, promptRunNames[r], len(a.Facts.Entities), len(a.Facts.Relations),
				strings.Join(by, "\t"))
		}
	}
}

// writePromptTriples пишет каждую связь каждого прогона: кусок, прогон, источник,
// корзина типа (relBucketNames), цель — имена как их написала модель. Нужен
// глазам: счёт по корзинам не говорит, натяжка добавленная связь или правда.
// Прогон без ответа строк не даёт.
func writePromptTriples(w io.Writer, chunks []poolChunk, answers [][]poolAnswer) {
	fmt.Fprintln(w, "кусок\tпрогон\tисточник\tтип\tцель")
	for i, ch := range chunks {
		for r := 0; r < promptRuns; r++ {
			a := answers[i][r]
			if !a.OK {
				continue
			}
			for _, rel := range a.Facts.Relations {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", ch.Key, promptRunNames[r], oneLine(rel.Src),
					relBucketNames[relBucket(rel.Type)], oneLine(rel.Dst))
			}
		}
	}
}

// oneLine — имя без табуляций и переводов строки, чтобы не ломать TSV.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// promptOpts — настройки замера без привязки к конфигу и коллекции.
type promptOpts struct {
	Candidate string
	Workers   int       // 0 — dirDefaultWorkers
	TSV       io.Writer // nil — не писать
	Triples   io.Writer // nil — не писать
	Progress  io.Writer // nil — не печатать ход
}

// measurePrompt — весь замер оси на готовых кусках и извлекателе. Отдельно от
// stabilityPrompt, чтобы тест шёл тем же путём, что и работа, не открывая
// коллекцию и не трогая сервер.
func measurePrompt(ctx context.Context, stdout io.Writer, chunks []poolChunk, ex graph.Extractor, o promptOpts) error {
	if strings.TrimSpace(o.Candidate) == "" {
		return fmt.Errorf("промпт-кандидат пуст")
	}
	if len(chunks) == 0 {
		return fmt.Errorf("нет кусков прозы для замера")
	}
	progress := o.Progress
	if progress == nil {
		progress = io.Discard
	}
	workers := o.Workers
	if workers <= 0 {
		workers = dirDefaultWorkers
	}
	started := time.Now()

	fmt.Fprintf(stdout, "модель %s; рабочая температура из настроек; прогонов на кусок %d\n", ex.Model(), promptRuns)
	fmt.Fprintf(progress, "%s запросы к модели: %d кусков × %d прогона = %d запросов, параллельно %d\n",
		time.Now().Format("15:04:05"), len(chunks), promptRuns, len(chunks)*promptRuns, workers)
	systems := []string{graph.SystemPrompt, graph.SystemPrompt, o.Candidate, o.Candidate}
	answers := askRuns(ctx, ex, systems, chunks, workers, progress)

	if o.TSV != nil {
		writePromptTSV(o.TSV, chunks, answers)
	}
	if o.Triples != nil {
		writePromptTriples(o.Triples, chunks, answers)
	}
	for r := 0; r < promptRuns; r++ {
		printPromptStat(stdout, promptRunNames[r], tallyPrompt(answers, r))
		sec, n := meanSeconds(answers, r)
		printRunSeconds(stdout, sec, n)
	}
	if err := firstAnswerErr(answers); err != nil {
		fmt.Fprintf(stdout, "первый сбой запроса или разбора: %v\n", err)
	}
	for i, p := range promptRunPairs {
		j, n := promptJaccard(answers, p[0], p[1])
		if i == 0 {
			fmt.Fprintf(stdout, "устойчивость: кусков с ответами всех %d прогонов: %d\n", promptRuns, n)
		}
		a, b := promptRunNames[p[0]], promptRunNames[p[1]]
		fmt.Fprintf(stdout, "Жаккар имён понятий, %s против %s: %.3f\n", a, b, j.Names)
		fmt.Fprintf(stdout, "Жаккар связей (пара имён и тип), %s против %s: %.3f\n", a, b, j.Pairs)
		fmt.Fprintf(stdout, "Жаккар направленных троек, %s против %s: %.3f\n", a, b, j.Triples)
	}
	fmt.Fprintln(stdout, "связи: «связано» — так назвала сама модель; «не назван/неизвестный» — пустой или незнакомый тип (RelTypeStrict отказывает обоим); Жаккар двух пустых наборов — 1")
	fmt.Fprintf(stdout, "замер занял %s\n", time.Since(started).Round(time.Second))
	return nil
}

// promptAxisArgs — ключи оси, как они пришли из командной строки; N, Out и
// Timeout добавляет диспетчер runStability из общих ключей режима.
type promptAxisArgs struct {
	URL, PromptFile string
	Workers         int
	N               int
	Out             string
	Triples         string // -triples: файл со всеми связями прогонов; пусто — не писать
	Timeout         time.Duration
}

// proseChunks — куски обычной прозы: без кода, оглавлений и литературы, не
// короче 600 знаков, n штук равномерно по обходу. Отбор тот же, что в
// stabilityTemp (там он записан в теле функции).
func proseChunks(c *kb.Collection, n int) ([]kb.ChunkInfo, error) {
	var cand []kb.ChunkInfo
	err := c.EachChunk(kb.ChunkFilter{}, func(ci kb.ChunkInfo) error {
		if ci.Code || ci.TOC || ci.Refs || len(ci.Text) < 600 {
			return nil
		}
		cand = append(cand, ci)
		return nil
	})
	return thinChunks(cand, n), err
}

// stabilityPrompt — ось prompt: проверка ключей, отбор кусков, извлекатель,
// сам замер.
func stabilityPrompt(stdout io.Writer, cfg *config.Config, collName string, a promptAxisArgs) error {
	// Ключи проверяются до открытия чего-либо.
	if a.URL == "" {
		return fmt.Errorf("оси prompt нужен -url: адрес сервера сборки; запасного адреса нет — в рабочем конфиге на первом месте Ollama, а сборка идёт в llama.cpp")
	}
	if a.PromptFile == "" {
		return fmt.Errorf("оси prompt нужен промпт-кандидат: -prompt-file <файл>")
	}
	candidate, err := readPromptFile(a.PromptFile, "промпт-кандидат")
	if err != nil {
		return err
	}
	progress := io.Writer(os.Stderr)

	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	fmt.Fprintf(progress, "%s отбираю куски прозы по всей коллекции (на большой — до минуты)\n", time.Now().Format("15:04:05"))
	infos, err := proseChunks(c, a.N)
	if err != nil {
		return err
	}
	chunks := make([]poolChunk, 0, len(infos))
	books := map[string]bool{}
	for _, ci := range infos {
		chunks = append(chunks, poolChunkOf(ci))
		books[ci.Book.Path] = true
	}

	// Адрес — только -url: graph.url из настроек в ExtractOptions перебил бы
	// запасной адрес New (см. runDirection).
	o := cfg.Graph.ExtractOptions()
	o.URL = a.URL
	o.Workers = a.Workers
	if o.Workers <= 0 {
		o.Workers = dirDefaultWorkers
	}
	o.Temperature = cfg.Graph.Temperature
	ex := graphex.New(o, a.URL, a.Timeout, nil)
	if ex == nil {
		return fmt.Errorf("извлечение не настроено: задайте graph.model")
	}
	fmt.Fprintf(stdout, "ось prompt: коллекция %s; к замеру: %d кусков из %d книг; сервер %s; промпт-кандидат %s (%d знаков)\n",
		collName, len(chunks), len(books), ex.URL(), filepath.Base(a.PromptFile), len([]rune(candidate)))

	var tsv io.Writer
	if a.Out != "" {
		f, err := os.Create(a.Out)
		if err != nil {
			return err
		}
		defer f.Close()
		tsv = f
	}
	var triples io.Writer
	if a.Triples != "" {
		f, err := os.Create(a.Triples)
		if err != nil {
			return err
		}
		defer f.Close()
		triples = f
	}
	return measurePrompt(context.Background(), stdout, chunks, ex, promptOpts{
		Candidate: candidate, Workers: a.Workers, TSV: tsv, Triples: triples, Progress: progress,
	})
}
