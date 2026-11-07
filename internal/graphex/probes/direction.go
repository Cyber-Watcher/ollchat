// direction.go — режим `-only direction`: в какую сторону модель ставит связь
// «является» или «часть» между двумя понятиями — старым промптом и промптом-
// кандидатом (этап 126, Я1: у рабочего графа books встречных пар «часть»
// 10565, «является» 2067). Вероятная причина — extract.txt, правило 5: промпт
// называет типы, но не говорит, какая сторона целое или общее.
//
// **Почему новый режим, а не ось stability.** stability берёт прозу равномерно
// по библиотеке и меряет устойчивость имён между прогонами. Здесь куски
// выбираются не равномерно, а как ДОКАЗАТЕЛЬСТВА встречных пар рабочего графа
// (те самые куски, откуда пришли A→B и B→A), и сравнивается не состав
// понятий, а сторона связи между теми же двумя понятиями. Общее со stability —
// только кусок, пользовательский промпт, извлекатель и Жаккар имён; их режим
// берёт те же вызовами.
//
// **Ход замера.** Рабочий граф открывается на чтение (как у det). По живым
// понятиям собираются направленные пары «является» и «часть» с их
// подтверждениями (тот же обход, что у Graph.HierarchyCheck: склейки
// применены, петли отброшены). Встречные пары (A<B, есть и A→B, и B→A одного
// типа) отбираются детерминированно по -seed. С каждой стороны пары берутся
// до -per кусков-доказательств. Каждый разный кусок отправляется модели трижды:
// старым промптом дважды и кандидатом раз, при рабочей температуре. Ответ
// разбирается ParseFacts, имена понятий — в номера тем же путём, что кладёт
// сборка (writeFacts): Entities.Lookup по написанию, затем Merges.Resolve.
//
// **Чего режим не делает.** Ничего не пишет ни в граф, ни в коллекцию. Не
// повторяет смысловую привязку докатки (linkNew по векторам): она требует
// записи и карты эмбеддера, поэтому имя, которого нет в реестре, здесь
// считается «не опознанным», а не привязывается к ближайшему понятию. Карта
// нужна: идут запросы к модели извлечения.
package probes

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/graphex"
)

// Умолчания режима: ноль в ключе читается как они, отрицательное значение
// снимает предел.
const (
	dirDefaultPairs   = 60
	dirDefaultPer     = 2
	dirDefaultWorkers = 6
	// dirTilt — во сколько раз записей одной стороны пары должно быть больше,
	// чтобы пара считалась «с перевесом».
	dirTilt = 3
)

// dirRuns — прогонов на кусок: старый промпт дважды и кандидат.
const dirRuns = 3

// dirRunNames — имена прогонов в итоге и в TSV; порядок совпадает с индексом.
var dirRunNames = [dirRuns]string{"старый-1", "старый-2", "кандидат"}

// dirOutcome — чем кончился один кусок-прогон для пары (A, B, T).
type dirOutcome int

const (
	outAB    dirOutcome = iota // связь типа T только A→B
	outBA                      // связь типа T только B→A
	outBoth                    // обе стороны в одном ответе
	outOther                   // связь A–B есть, но не типа T
	outNone                    // связи между ними нет или имена не опознаны
	outFail                    // запрос или разбор не удался: исхода нет
)

// outCounted — сколько исходов считается в итоге (без сбоя).
const outCounted = int(outFail)

func (o dirOutcome) String() string {
	switch o {
	case outAB:
		return "A→B"
	case outBA:
		return "B→A"
	case outBoth:
		return "обе"
	case outOther:
		return "другой тип"
	case outNone:
		return "нет"
	}
	return "сбой"
}

// dirPair — встречная пара рабочего графа: оба направления одного типа.
type dirPair struct {
	A, B uint32 // A < B
	Type uint8  // graph.RelIs или graph.RelPart
	// AB и BA — подтверждения каждой стороны, по одному на запись связи
	// (повторы кусков остаются: по их числу судят о перевесе).
	AB, BA []graph.ChunkKey
}

// tilt: +1 — записей A→B не меньше чем в dirTilt раз больше, чем B→A; -1 —
// наоборот; 0 — перевеса нет.
func (p dirPair) tilt() int {
	switch {
	case len(p.AB) >= dirTilt*len(p.BA):
		return 1
	case len(p.BA) >= dirTilt*len(p.AB):
		return -1
	}
	return 0
}

// collectMutual собирает встречные пары «является» и «часть» с
// подтверждениями. Обход — по живым понятиям через Edges.OfAsWritten, как у
// Graph.HierarchyCheckAsWritten: концы связей уже переведены к выжившим
// склейки, петли отброшены, а правило перевеса выдачи не действует — прибор
// меряет, что записала модель, и перевес считает сам (tilt). Порядок
// результата — по (тип, A, B), не по обходу карты.
func collectMutual(g *graph.Graph) []dirPair {
	type dkey struct {
		src, dst uint32
		typ      uint8
	}
	ev := map[dkey][]graph.ChunkKey{}
	for _, ent := range g.Entities().Live() {
		for _, ed := range g.Edges().OfAsWritten(ent.ID) {
			if ed.Type != graph.RelIs && ed.Type != graph.RelPart {
				continue
			}
			k := dkey{ed.Src, ed.Dst, ed.Type}
			ev[k] = append(ev[k], ed.Evidence)
		}
	}
	var out []dirPair
	for k, ab := range ev {
		if k.src >= k.dst {
			continue
		}
		ba, ok := ev[dkey{k.dst, k.src, k.typ}]
		if !ok {
			continue
		}
		out = append(out, dirPair{A: k.src, B: k.dst, Type: k.typ, AB: ab, BA: ba})
	}
	sortPairs(out)
	return out
}

// sortPairs — порядок пар: тип, затем A, затем B.
func sortPairs(list []dirPair) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.A != b.A {
			return a.A < b.A
		}
		return a.B < b.B
	})
}

// sampleMutual — детерминированная выборка n пар: список сортируется,
// перемешивается rand.New(rand.NewSource(seed)), берутся первые n. Входной
// порядок на результат не влияет. n == 0 — dirDefaultPairs, n < 0 — все пары.
func sampleMutual(all []dirPair, n int, seed int64) []dirPair {
	list := append([]dirPair(nil), all...)
	sortPairs(list)
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
	if n == 0 {
		n = dirDefaultPairs
	}
	if n > 0 && n < len(list) {
		list = list[:n]
	}
	return list
}

// pickEvidence — до per разных кусков из подтверждений по возрастанию ключа
// (книга, затем номер), минуя куски из skip. per < 0 — без предела.
func pickEvidence(keys []graph.ChunkKey, per int, skip map[graph.ChunkKey]bool) []graph.ChunkKey {
	list := append([]graph.ChunkKey(nil), keys...)
	sort.Slice(list, func(i, j int) bool {
		if list[i].Doc != list[j].Doc {
			return list[i].Doc < list[j].Doc
		}
		return list[i].Ord < list[j].Ord
	})
	var out []graph.ChunkKey
	for i, k := range list {
		if (i > 0 && k == list[i-1]) || skip[k] {
			continue
		}
		if per >= 0 && len(out) >= per {
			break
		}
		out = append(out, k)
	}
	return out
}

// dirSide — с какой стороны пары взят кусок-доказательство.
type dirSide uint8

const (
	sideAB dirSide = iota
	sideBA
)

func (s dirSide) String() string {
	if s == sideAB {
		return "A→B"
	}
	return "B→A"
}

// dirCase — один кусок-доказательство одной пары.
type dirCase struct {
	Pair int // индекс в выбранных парах
	Key  graph.ChunkKey
	Side dirSide
}

// pairCases — куски пары: до per с A→B, затем до per с B→A; кусок, уже взятый
// с первой стороны, на второй не повторяется.
func pairCases(idx int, p dirPair, per int) []dirCase {
	var out []dirCase
	taken := map[graph.ChunkKey]bool{}
	for _, k := range pickEvidence(p.AB, per, nil) {
		taken[k] = true
		out = append(out, dirCase{Pair: idx, Key: k, Side: sideAB})
	}
	for _, k := range pickEvidence(p.BA, per, taken) {
		out = append(out, dirCase{Pair: idx, Key: k, Side: sideBA})
	}
	return out
}

// dirEdge — связь из ответа модели, уже в номерах понятий.
type dirEdge struct {
	Src, Dst uint32
	Type     uint8
}

// answerEdges переводит разобранный ответ в связи между номерами понятий тем
// же путём, что writeFacts сборки, но только поиском: имя понятия ищется в
// реестре по написанию (Entities.Lookup, он же ключ Entities.Add), номер
// переводится к выжившему склейки (Merges.Resolve), конец связи берётся из
// понятий того же ответа. Тип — RelType, у графа формата 2 — RelTypeStrict
// (неназванный тип связь не пишет). Не найденное в реестре отпадает; петли
// после склейки отбрасываются, как при чтении графа.
func answerEdges(g *graph.Graph, f graph.Facts) []dirEdge {
	ids := make(map[string]uint32, len(f.Entities))
	for _, e := range f.Entities {
		ent, ok := g.Entities().Lookup(e.Name)
		if !ok {
			continue
		}
		ids[graph.Normalize(e.Name)] = g.Merges().Resolve(ent.ID)
	}
	strict := g.Meta().Version >= graph.FormatV2
	var out []dirEdge
	for _, r := range f.Relations {
		src, okSrc := ids[graph.Normalize(r.Src)]
		dst, okDst := ids[graph.Normalize(r.Dst)]
		if !okSrc || !okDst || src == dst {
			continue
		}
		typ := graph.RelType(r.Type)
		if strict {
			var named bool
			if typ, named = graph.RelTypeStrict(r.Type); !named {
				continue
			}
		}
		out = append(out, dirEdge{Src: src, Dst: dst, Type: typ})
	}
	return out
}

// classifyDirection — исход одного ответа для пары (a, b) и типа typ.
// Сторона нужного типа важнее связи другого типа между теми же понятиями.
func classifyDirection(edges []dirEdge, a, b uint32, typ uint8) dirOutcome {
	var ab, ba, other bool
	for _, e := range edges {
		switch {
		case e.Src == a && e.Dst == b:
			if e.Type == typ {
				ab = true
			} else {
				other = true
			}
		case e.Src == b && e.Dst == a:
			if e.Type == typ {
				ba = true
			} else {
				other = true
			}
		}
	}
	switch {
	case ab && ba:
		return outBoth
	case ab:
		return outAB
	case ba:
		return outBA
	case other:
		return outOther
	}
	return outNone
}

// dirTally — итог одного прогона (старый-1, старый-2 или кандидат).
type dirTally struct {
	Cases    int             // кусков-прогонов с ответом
	Failed   int             // сбоев запроса или разбора
	Outcomes [outCounted]int // исходы по видам
	// PairsAnswered — пар, у которых в этом прогоне есть хоть один ответ;
	// PairsMutual — из них оставшихся встречными: по всем кускам пары есть
	// и A→B, и B→A нужного типа.
	PairsAnswered, PairsMutual int
	// Перевес: пар, где записей одной стороны в рабочем графе не меньше чем в
	// dirTilt раз больше; исходы A→B/B→A этих пар в сторону перевеса и против.
	TiltPairs, TiltToward, TiltAgainst int
}

// tallyRun считает итог прогона run по исходам outs (параллельны cases).
func tallyRun(pairs []dirPair, cases []dirCase, outs [][dirRuns]dirOutcome, run int) dirTally {
	var t dirTally
	sawAB := make([]bool, len(pairs))
	sawBA := make([]bool, len(pairs))
	answered := make([]bool, len(pairs))
	for i, c := range cases {
		out := outs[i][run]
		if out == outFail {
			t.Failed++
			continue
		}
		t.Cases++
		t.Outcomes[out]++
		answered[c.Pair] = true
		if out == outAB || out == outBoth {
			sawAB[c.Pair] = true
		}
		if out == outBA || out == outBoth {
			sawBA[c.Pair] = true
		}
		if tilt := pairs[c.Pair].tilt(); tilt != 0 && (out == outAB || out == outBA) {
			if (tilt > 0) == (out == outAB) {
				t.TiltToward++
			} else {
				t.TiltAgainst++
			}
		}
	}
	for i, p := range pairs {
		if !answered[i] {
			continue
		}
		t.PairsAnswered++
		if sawAB[i] && sawBA[i] {
			t.PairsMutual++
		}
		if p.tilt() != 0 {
			t.TiltPairs++
		}
	}
	return t
}

// nameKeys — набор имён понятий ответа в виде, годном для Жаккара.
func nameKeys(f graph.Facts) map[string]bool {
	out := make(map[string]bool, len(f.Entities))
	for _, e := range f.Entities {
		out[graph.MatchName(e.Name)] = true
	}
	return out
}

// meanJaccard — средний Жаккар имён прогонов a и b по кускам, где ответили все
// dirRuns прогонов (одни и те же куски для обеих пар прогонов); второй результат —
// сколько таких кусков.
func meanJaccard(answers [][]poolAnswer, a, b int) (float64, int) {
	sum, n := 0.0, 0
	for _, ans := range answers {
		if !allAnswered(ans) {
			continue
		}
		sum += jaccard(nameKeys(ans[a].Facts), nameKeys(ans[b].Facts))
		n++
	}
	if n == 0 {
		return 0, 0
	}
	return sum / float64(n), n
}

// directionOpts — настройки замера без привязки к конфигу и коллекции.
type directionOpts struct {
	Pairs, Per int   // 0 — умолчание, отрицательное — без предела
	Seed       int64 // зерно выборки пар
	Workers    int   // 0 — dirDefaultWorkers
	Candidate  string
	// Rel — брать встречные пары только этого типа (graph.RelIs или
	// graph.RelPart); 0 — оба типа.
	Rel uint8
	// Base — промпт, которым идут «старый-1» и «старый-2» вместо
	// graph.SystemPrompt; пусто — рабочий. BaseName — имя его файла для шапки.
	Base, BaseName string
	TSV            io.Writer // nil — не писать
	Progress       io.Writer // nil — не печатать ход
}

// parseRel — значение ключа -rel: пусто — оба типа (0), иначе номер типа.
func parseRel(s string) (uint8, error) {
	switch strings.TrimSpace(s) {
	case "":
		return 0, nil
	case "является":
		return graph.RelIs, nil
	case "часть":
		return graph.RelPart, nil
	}
	return 0, fmt.Errorf("неизвестный тип связи в -rel: %q; есть: является, часть (пусто — оба)", s)
}

// measureDirection — весь замер на уже открытых графе, источнике кусков и
// извлекателе. Отдельно от runDirection, чтобы тест шёл тем же путём, что и
// работа, не открывая коллекцию и не трогая сервер.
func measureDirection(ctx context.Context, stdout io.Writer, g *graph.Graph, chunkOf func(graph.ChunkKey) (poolChunk, bool), ex graph.Extractor, o directionOpts) error {
	if strings.TrimSpace(o.Candidate) == "" {
		return fmt.Errorf("промпт-кандидат пуст")
	}
	if o.Base != "" && strings.TrimSpace(o.Base) == "" {
		return fmt.Errorf("базовый промпт (-base-file) пуст")
	}
	if o.Rel != 0 && o.Rel != graph.RelIs && o.Rel != graph.RelPart {
		return fmt.Errorf("-rel: тип %q не бывает встречным, есть только является и часть", graph.RelName(o.Rel))
	}
	progress := o.Progress
	if progress == nil {
		progress = io.Discard
	}
	say := func(format string, a ...any) {
		fmt.Fprintf(progress, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
	}
	per, workers := o.Per, o.Workers
	if per == 0 {
		per = dirDefaultPer
	}
	if workers <= 0 {
		workers = dirDefaultWorkers
	}
	started := time.Now()

	say("собираю направленные пары «является» и «часть» с подтверждениями (на большом графе — десятки секунд)")
	all := collectMutual(g)
	nIs, nPart := 0, 0
	for _, p := range all {
		if p.Type == graph.RelIs {
			nIs++
		} else {
			nPart++
		}
	}
	fmt.Fprintf(stdout, "модель %s; рабочая температура из настроек\n", ex.Model())
	if o.Rel != 0 {
		fmt.Fprintf(stdout, "тип связи: только «%s» (-rel)\n", graph.RelName(o.Rel))
	}
	oldPrompt := graph.SystemPrompt
	if o.Base != "" {
		oldPrompt = o.Base
		fmt.Fprintf(stdout, "старый промпт подменён (-base-file): %s, %d знаков; graph.SystemPrompt в прогонах не участвует\n",
			o.BaseName, len([]rune(o.Base)))
	}
	fmt.Fprintf(stdout, "встречных пар в графе: является %d, часть %d\n", nIs, nPart)
	if len(all) == 0 {
		return fmt.Errorf("в графе нет встречных пар «является» и «часть»: мерить нечего")
	}
	pool := all
	if o.Rel != 0 {
		pool = nil
		for _, p := range all {
			if p.Type == o.Rel {
				pool = append(pool, p)
			}
		}
		if len(pool) == 0 {
			return fmt.Errorf("в графе нет встречных пар типа «%s»: мерить нечего", graph.RelName(o.Rel))
		}
	}

	say("отбираю пары (зерно %d) и куски-доказательства", o.Seed)
	pairs := sampleMutual(pool, o.Pairs, o.Seed)
	selIs := 0
	for _, p := range pairs {
		if p.Type == graph.RelIs {
			selIs++
		}
	}
	fmt.Fprintf(stdout, "выбрано пар: %d (зерно %d): является %d, часть %d\n", len(pairs), o.Seed, selIs, len(pairs)-selIs)

	var cases []dirCase
	for i, p := range pairs {
		cases = append(cases, pairCases(i, p, per)...)
	}
	// Разные куски — в порядке первого появления; текст берётся один раз.
	index := map[graph.ChunkKey]int{}
	var chunks []poolChunk
	missing := map[graph.ChunkKey]bool{}
	kept := cases[:0]
	for _, c := range cases {
		if _, ok := index[c.Key]; !ok && !missing[c.Key] {
			ch, found := chunkOf(c.Key)
			if !found {
				missing[c.Key] = true
			} else {
				index[c.Key] = len(chunks)
				chunks = append(chunks, ch)
			}
		}
		if !missing[c.Key] {
			kept = append(kept, c)
		}
	}
	cases = kept
	fmt.Fprintf(stdout, "кусков-доказательств: %d (разных кусков %d, нет в коллекции %d)\n", len(cases), len(chunks), len(missing))
	if len(cases) == 0 {
		return fmt.Errorf("ни одного куска-доказательства в коллекции: мерить нечего")
	}

	say("запросы к модели: %d кусков × %d прогона = %d запросов, параллельно %d", len(chunks), dirRuns, len(chunks)*dirRuns, workers)
	systems := []string{oldPrompt, oldPrompt, o.Candidate}
	answers := askRuns(ctx, ex, systems, chunks, workers, progress)

	say("сверяю ответы с рабочим графом")
	outs := make([][dirRuns]dirOutcome, len(cases))
	for i, c := range cases {
		p := pairs[c.Pair]
		ans := answers[index[c.Key]]
		for r := 0; r < dirRuns; r++ {
			if !ans[r].OK {
				outs[i][r] = outFail
				continue
			}
			outs[i][r] = classifyDirection(answerEdges(g, ans[r].Facts), p.A, p.B, p.Type)
		}
	}

	if o.TSV != nil {
		fmt.Fprintln(o.TSV, "кусок\tA\tB\tтип\tсторона\tпрогон\tисход")
		clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")
		name := func(id uint32) string {
			e, _ := g.Entities().Get(id)
			return clean.Replace(e.Name)
		}
		for i, c := range cases {
			p := pairs[c.Pair]
			for r := 0; r < dirRuns; r++ {
				fmt.Fprintf(o.TSV, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.Key, name(p.A), name(p.B), graph.RelName(p.Type),
					c.Side, dirRunNames[r], outs[i][r])
			}
		}
	}

	for r := 0; r < dirRuns; r++ {
		printDirTally(stdout, dirRunNames[r], tallyRun(pairs, cases, outs, r))
		sec, n := meanSeconds(answers, r)
		printRunSeconds(stdout, sec, n)
	}
	if err := firstAnswerErr(answers); err != nil {
		fmt.Fprintf(stdout, "первый сбой запроса или разбора: %v\n", err)
	}
	j12, n12 := meanJaccard(answers, 0, 1)
	j13, _ := meanJaccard(answers, 0, 2)
	fmt.Fprintf(stdout, "устойчивость: кусков с ответами всех трёх прогонов: %d\n", n12)
	fmt.Fprintf(stdout, "Жаккар имён понятий, %s против %s: %.3f\n", dirRunNames[0], dirRunNames[1], j12)
	fmt.Fprintf(stdout, "Жаккар имён понятий, %s против %s: %.3f\n", dirRunNames[0], dirRunNames[2], j13)
	fmt.Fprintln(stdout, "исходы: A→B и B→A — связь нужного типа в одну сторону; обе — в обе стороны в одном ответе; другой тип — между понятиями есть связь иного типа; нет — связи нет или имена не нашлись в реестре")
	fmt.Fprintf(stdout, "замер занял %s\n", time.Since(started).Round(time.Second))
	return nil
}

// printDirTally печатает итог одного прогона, по строке на число.
func printDirTally(w io.Writer, name string, t dirTally) {
	fmt.Fprintf(w, "прогон %s\n", name)
	fmt.Fprintf(w, "  кусков-прогонов: %d\n", t.Cases)
	fmt.Fprintf(w, "  сбоев запроса или разбора: %d\n", t.Failed)
	for o := 0; o < outCounted; o++ {
		fmt.Fprintf(w, "  исход %s: %d\n", dirOutcome(o), t.Outcomes[o])
	}
	fmt.Fprintf(w, "  пар с ответом: %d\n", t.PairsAnswered)
	fmt.Fprintf(w, "  пар, оставшихся встречными: %d\n", t.PairsMutual)
	fmt.Fprintf(w, "  пар с перевесом (записей одной стороны в рабочем графе не меньше чем в %d раза больше): %d\n", dirTilt, t.TiltPairs)
	if d := t.TiltToward + t.TiltAgainst; d > 0 {
		fmt.Fprintf(w, "  по перевесу: в сторону перевеса %d из %d исходов A→B/B→A (%.1f%%)\n",
			t.TiltToward, d, 100*float64(t.TiltToward)/float64(d))
	} else {
		fmt.Fprintf(w, "  по перевесу: исходов A→B/B→A у пар с перевесом нет\n")
	}
}

// directionArgs — ключи режима, как они пришли из командной строки.
type directionArgs struct {
	URL, PromptFile, Out string
	Rel, BaseFile        string // -rel и -base-file; пусто — не заданы
	Pairs, Per, Workers  int
	Seed                 int64
	Timeout              time.Duration
}

// readPromptFile читает промпт из файла; нет файла или он пуст — ошибка.
// what называет промпт в сообщении.
func readPromptFile(path, what string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", what, err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", fmt.Errorf("%s %s пуст", what, path)
	}
	return string(raw), nil
}

// readCandidate читает промпт-кандидат; нет ключа, файла или он пуст — ошибка.
func readCandidate(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("режиму direction нужен промпт-кандидат: -prompt-file <файл>")
	}
	return readPromptFile(path, "промпт-кандидат")
}

// runDirection — режим direction: проверка ключей, открытие коллекции и графа
// на чтение, извлекатель, сам замер.
func runDirection(stdout io.Writer, cfg *config.Config, collName string, a directionArgs) error {
	// Ключи проверяются до открытия чего-либо: ошибка в них не должна стоить
	// получаса открытия графа.
	if a.URL == "" {
		return fmt.Errorf("режиму direction нужен -url: адрес сервера сборки; запасного адреса нет — в рабочем конфиге на первом месте Ollama, а сборка идёт в llama.cpp")
	}
	candidate, err := readCandidate(a.PromptFile)
	if err != nil {
		return err
	}
	rel, err := parseRel(a.Rel)
	if err != nil {
		return err
	}
	baseText := ""
	if a.BaseFile != "" {
		if baseText, err = readPromptFile(a.BaseFile, "базовый промпт (-base-file)"); err != nil {
			return err
		}
	}
	progress := io.Writer(os.Stderr)

	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	fmt.Fprintf(progress, "%s открываю граф — на большом это полминуты\n", time.Now().Format("15:04:05"))
	var mu sync.Mutex
	last := ""
	g, err := graph.OpenWithProgress(c.Dir(), c.ChunkCount(), cfg.Graph.Rules(), func(p graph.OpenProgress) {
		mu.Lock()
		defer mu.Unlock()
		if p.Stage != last {
			last = p.Stage
			fmt.Fprintf(progress, "%s   граф: %s\n", time.Now().Format("15:04:05"), p.Stage)
		}
	})
	if err != nil {
		return err
	}
	defer g.Close()

	// graph.url из настроек в ExtractOptions перебил бы запасной адрес New, а
	// -url обязан быть главным: замер уходит туда, куда сказано.
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
	fmt.Fprintf(stdout, "направление связей: коллекция %s, граф рабочий; сервер %s; промпт-кандидат %s (%d знаков)\n",
		collName, ex.URL(), filepath.Base(a.PromptFile), len([]rune(candidate)))

	var tsv io.Writer
	if a.Out != "" {
		f, err := os.Create(a.Out)
		if err != nil {
			return err
		}
		defer f.Close()
		tsv = f
	}
	chunkOf := func(k graph.ChunkKey) (poolChunk, bool) {
		ci, ok := c.ChunkByRef(k.Doc, k.Ord)
		if !ok {
			return poolChunk{}, false
		}
		return poolChunkOf(ci), true
	}
	return measureDirection(context.Background(), stdout, g, chunkOf, ex, directionOpts{
		Pairs: a.Pairs, Per: a.Per, Seed: a.Seed, Workers: a.Workers,
		Candidate: candidate, Rel: rel, Base: baseText, BaseName: filepath.Base(a.BaseFile),
		TSV: tsv, Progress: progress,
	})
}
