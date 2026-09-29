package stats

// Замер формулы локальности для ВЫДАЧИ СВЯЗЕЙ — этап 89, долг на 16.09.2026.
//
// **Задача.** Понятие, растянутое по многим книгам (`quality` — 29 книг,
// `Metrics` — сотни связей из всех каталогов), тащит свои связи в ответ
// наравне со специфичным. Замер 15.09.2026: на методологических вопросах
// 26.9% связей подтверждены книгами чужого каталога, 6 вопросов из 20
// засорены больше чем на треть.
//
// **Почему замер, а не сразу правка.** Правка сортировки связей — это правка
// ядра поиска, которым пользуются `/search`, инструменты и подмешивание.
// Прежде чем её делать, надо знать, какая формула помогает и не платим ли мы
// за чистоту потерей полезного. Здесь обе меры считаются на готовом графе,
// без карты (кроме вектора вопроса) и без единой правки кода поиска:
// выдача берётся штатным `find.Search` с широким пулом и пересортировывается
// по каждой формуле — ровно так, как это делал бы код после правки.
//
// **Две меры, и вторая важнее первой.**
//   1. Доля связей из чужих каталогов на методологическом наборе — должна упасть.
//   2. Доля пар «как связаны X и Y», у которых прямая связь X↔Y осталась
//      в выдаче — **не должна упасть**. Улучшать один сценарий за счёт
//      другого нельзя (условие владельца 15.09.2026).

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

// relPairRe достаёт пары понятий из graph_relations.toml: [[case]] с a и b.
var queryQuestionRe = regexp.MustCompile(`(?m)^\s*query\s*=\s*"([^"]+)"`)

var relPairRe = regexp.MustCompile(`(?ms)\[\[case\]\].*?a\s*=\s*"([^"]+)".*?b\s*=\s*"([^"]+)"`)

// localityFormula — способ понизить связь, ведущую к «растянутому» понятию.
type localityFormula struct {
	name string
	// score считает место связи: больше — выше в выдаче.
	// deg — сколько связей у дальнего конца, books — в скольких он книгах,
	// inside — оба ли конца связи входят в понятия самой выдачи.
	score func(weight float64, deg, books int, inside bool) float64
}

var localityFormulas = []localityFormula{
	{"как сейчас (вес)", func(w float64, _, _ int, _ bool) float64 { return w }},
	{"вес / log2(2+степень)", func(w float64, deg, _ int, _ bool) float64 {
		return w / math.Log2(2+float64(deg))
	}},
	{"вес / log2(2+книг)", func(w float64, _, books int, _ bool) float64 {
		return w / math.Log2(2+float64(books))
	}},
	{"вес / sqrt(степень)", func(w float64, deg, _ int, _ bool) float64 {
		return w / math.Sqrt(1+float64(deg))
	}},
	// Связь МЕЖДУ понятиями выдачи — вперёд, остальное как было.
	//
	// Мысль пришла из первого прогона: у базовой формулы сохранилось лишь
	// 11 прямых связей из 60 пар, хотя связь в графе есть у 30 из них.
	// То есть связь, о которой прямо спросили, вытесняют более тяжёлые
	// соседи — а она и есть ответ на вопрос.
	{"связи внутри выдачи вперёд", func(w float64, _, _ int, inside bool) float64 {
		if inside {
			return w + 1e9 // порядок, а не вес: важна только полка
		}
		return w
	}},
	{"внутри вперёд + /log2(степень)", func(w float64, deg, _ int, inside bool) float64 {
		s := w / math.Log2(2+float64(deg))
		if inside {
			s += 1e9
		}
		return s
	}},
}

// localityStats — счётчики одной формулы.
type localityStats struct {
	relations int // связей показано всего
	alien     int // из них чужих: НИ ОДНА подтверждающая книга не из своей области
	dirtyQ    int // вопросов, где таких чужих больше 30%
	alienOld  int // прежняя мера: чужая ПЕРВАЯ выдержка (для сравнения)
	dirtyOld  int
	keptPairs int // пар X↔Y, где прямая связь осталась в выдаче
}

// localityEval — главный ход замера.
//
// method — набор методологических вопросов, pairs — набор «как связаны X и Y»,
// want — какой каталог считать своим, pool — во сколько раз шире брать связи
// перед пересортировкой (правка отбирала бы из того же пула).
func localityEval(cfg *config.Config, g *graph.Graph, c *kb.Collection,
	method, pairs, want string, pool int) {
	// Подсказка в шапке: с каким графом считаем — с подъёмом связей между
	// понятиями вопроса или без него. Иначе два прогона не различить.
	if g.Rules().SeedRelationsOff {
		fmt.Println("\n  ВНИМАНИЕ: подъём связей между понятиями вопроса ВЫКЛЮЧЕН (SeedRelationsOff)")
	}

	questions := readMethodQuestions(method)
	if len(questions) == 0 {
		fmt.Println("в методологическом наборе не нашлось строк вида text = \"…\"")
		return
	}
	// Своя область — у каждого вопроса своя, если набор её называет (поле
	// folder); `want` — только запасное значение (аудит 17.09.2026, S11).
	ownFolder := readQuestionFolders(method)
	names := readRelationPairs(pairs)

	byDoc := map[uint32]string{}
	roots := append(append([]string{}, cfg.KB.Roots...), c.Roots()...) // корни библиотеки, затем коллекции
	for _, b := range c.MatchingDocs(kb.ChunkFilter{}) {
		byDoc[b.ID] = folderOf(b.Path, roots)
	}

	deps := find.Deps{
		Coll:     c,
		Graph:    g,
		Embedder: kbembed.New(cfg.KB.EmbedOptions(), cfg.EmbedFallback(), 2*time.Minute, nil),
	}
	// Штатное число связей на понятие — то, что увидит человек; пул шире,
	// потому что пересортировка имеет смысл, только когда есть из чего выбирать.
	shown := cfg.Mix.Neighbors
	if shown <= 0 {
		shown = 5
	}

	fmt.Printf("\nЗамер локальности выдачи связей (этап 89; своя область — %s)\n", want)
	fmt.Printf("  методологических вопросов %d, пар «как связаны» %d\n", len(questions), len(names))
	fmt.Printf("  связей на понятие: показываем %d, пул для пересортировки %d\n\n", shown, shown*pool)

	stats := make([]localityStats, len(localityFormulas))
	wordsOnly := 0 // вопросов, где смысловой вход не отработал: числа с прежними не сравнимы

	// Часть 1: чужие каталоги на методологических вопросах.
	for _, q := range questions {
		res, err := find.Search(context.Background(), deps, q,
			find.Opts{Neighbors: shown * pool})
		if err != nil {
			fmt.Printf("  вопрос «%s» — ошибка: %v\n", cut(q, 40), err)
			continue
		}
		if res.WordsOnly {
			wordsOnly++
		}
		mine := want
		if f := ownFolder[q]; f != "" {
			mine = f
		}
		inside := entitySet(res.Entities)
		for i, f := range localityFormulas {
			kept := pickByFormula(g, res.Relations, f, shown, inside)
			alien, alienOld := 0, 0
			for _, rel := range kept {
				first, all := alienRelation(rel, byDoc, mine)
				if all {
					alien++
				}
				if first {
					alienOld++
				}
			}
			stats[i].relations += len(kept)
			stats[i].alien += alien
			stats[i].alienOld += alienOld
			if len(kept) > 0 && 100*float64(alien)/float64(len(kept)) >= 30 {
				stats[i].dirtyQ++
			}
			if len(kept) > 0 && 100*float64(alienOld)/float64(len(kept)) >= 30 {
				stats[i].dirtyOld++
			}
		}
	}

	// Часть 2: не теряются ли прямые связи названных понятий.
	for _, p := range names {
		q := fmt.Sprintf("Как связаны %s и %s?", p[0], p[1])
		res, err := find.Search(context.Background(), deps, q,
			find.Opts{Neighbors: shown * pool})
		if err != nil {
			continue
		}
		inside := entitySet(res.Entities)
		for i, f := range localityFormulas {
			kept := pickByFormula(g, res.Relations, f, shown, inside)
			if hasPair(kept, p[0], p[1]) {
				stats[i].keptPairs++
			}
		}
	}

	if wordsOnly > 0 {
		fmt.Printf("  ВНИМАНИЕ: у %d вопросов смысловой вход не отработал (карта занята?) — с прежними числами не сравнивать\n\n", wordsOnly)
	}
	fmt.Println("  формула                        связей  чужих        грязных   прежняя мера:        пары X↔Y")
	fmt.Println("                                         (все книги)  вопросов  первая выдержка      сохранены")
	for i, f := range localityFormulas {
		s := stats[i]
		share, shareOld := 0.0, 0.0
		if s.relations > 0 {
			share = 100 * float64(s.alien) / float64(s.relations)
			shareOld = 100 * float64(s.alienOld) / float64(s.relations)
		}
		fmt.Printf("  %-28s %7d %6d (%4.1f%%) %6d/%d %6d (%4.1f%%) %2d/%d %7d/%d\n",
			f.name, s.relations, s.alien, share, s.dirtyQ, len(questions),
			s.alienOld, shareOld, s.dirtyOld, len(questions),
			s.keptPairs, len(names))
	}
	fmt.Println()
	fmt.Println("  «Чужая» связь — НИ ОДНА из подтверждающих её книг (до 64) не из своей")
	fmt.Println("  области вопроса. Прежняя мера смотрела только первую выдержку: связь,")
	fmt.Println("  подтверждённая и своей, и чужой книгой, считалась чужой по случаю порядка.")
	fmt.Println("  Чужая связь — не ошибка графа: в книге по Kubernetes `Metrics` действительно связан")
	fmt.Println("  с Prometheus. Вопрос в том, уместно ли это в ответе про графы.")
	fmt.Println()
	fmt.Println("  Формула годится, только если чужих стало меньше, А ПАРЫ X↔Y")
	fmt.Println("  сохранились полностью: улучшать один сценарий за счёт другого")
	fmt.Println("  нельзя (условие владельца 15.09.2026).")
}

// alienRelation — чужая ли связь для области mine.
//
// first — прежняя мера: книга ПЕРВОЙ выдержки не из своей области.
// all — честная: ни одна подтверждающая книга не из своей области. Книги связи
// лежат в `Books` (по одной записи на книгу, до 64); у старой выдачи без него —
// в показанных выдержках.
func alienRelation(rel graph.FoundRelation, byDoc map[uint32]string, mine string) (first, all bool) {
	first = byDoc[rel.Evidence.Doc] != mine
	keys := rel.Books
	if len(keys) == 0 {
		keys = rel.Evidences
	}
	if len(keys) == 0 {
		return first, first
	}
	for _, k := range keys {
		if byDoc[k.Doc] == mine {
			return first, false
		}
	}
	return first, true
}

// questionFolderRe достаёт из набора пару «вопрос → своя область».
var questionFolderRe = regexp.MustCompile(`(?m)^\s*text\s*=\s*"([^"]+)"\s*\n\s*folder\s*=\s*"([^"]+)"`)

func readQuestionFolders(path string) map[string]string {
	raw, err := os.ReadFile(path)
	die(err)
	out := map[string]string{}
	for _, m := range questionFolderRe.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = m[2]
	}
	return out
}

// pickByFormula пересортировывает связи по формуле и оставляет по shown штук
// на каждое понятие-источник — так же, как это делал бы код выдачи.
func pickByFormula(g *graph.Graph, rels []graph.FoundRelation,
	f localityFormula, shown int, inside map[string]bool) []graph.FoundRelation {

	type keyed struct {
		rel   graph.FoundRelation
		score float64
		pos   int
	}
	bySrc := map[string][]keyed{}
	for i, r := range rels {
		deg, books := farEndSize(g, r)
		both := inside[strings.ToLower(r.Src)] && inside[strings.ToLower(r.Dst)]
		bySrc[r.Src] = append(bySrc[r.Src],
			keyed{r, f.score(float64(r.Weight), deg, books, both), i})
	}
	out := make([]graph.FoundRelation, 0, len(rels))
	for _, list := range bySrc {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].score != list[j].score {
				return list[i].score > list[j].score
			}
			return list[i].pos < list[j].pos // при равенстве — прежний порядок
		})
		if len(list) > shown {
			list = list[:shown]
		}
		for _, k := range list {
			out = append(out, k.rel)
		}
	}
	return out
}

// farEndSize — насколько «растянут» дальний конец связи: сколько у него связей
// и в скольких книгах он встречается.
func farEndSize(g *graph.Graph, r graph.FoundRelation) (deg, books int) {
	e, ok := g.Entities().Lookup(r.Dst)
	if !ok {
		return 0, 0
	}
	deg = len(g.Edges().Neighbors(e.ID))
	seen := map[uint32]bool{}
	for _, k := range g.Mentions().Of(e.ID) {
		seen[k.Doc] = true
	}
	return deg, len(seen)
}

// entitySet — имена и синонимы понятий выдачи, по которым узнаётся связь
// «внутри выдачи». Синонимы нужны потому, что связь хранит то написание,
// которым понятие названо в книге, а выдача — то, которым оно найдено.
func entitySet(ents []graph.FoundEntity) map[string]bool {
	out := make(map[string]bool, len(ents)*3)
	for _, e := range ents {
		out[strings.ToLower(e.Name)] = true
		for _, a := range e.Aliases {
			out[strings.ToLower(a)] = true
		}
	}
	return out
}

// hasPair — осталась ли в выдаче прямая связь между названными понятиями.
func hasPair(rels []graph.FoundRelation, a, b string) bool {
	for _, r := range rels {
		if (strings.EqualFold(r.Src, a) && strings.EqualFold(r.Dst, b)) ||
			(strings.EqualFold(r.Src, b) && strings.EqualFold(r.Dst, a)) {
			return true
		}
	}
	return false
}

// readMethodQuestions читает вопросы набора.
//
// Ключ у наборов разный и менять его задним числом нельзя: `graph_method_questions.toml`
// пишет `text = "…"`, а `graph_relations.toml` — `query = "…"`, и на него уже
// сняты опорные числа. Поэтому читаются оба: набор — это данные, и подгонять
// данные под инструмент неправильно.
func readMethodQuestions(path string) []string {
	raw, err := os.ReadFile(path)
	die(err)
	var out []string
	for _, m := range methodQuestionRe.FindAllStringSubmatch(string(raw), -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		for _, m := range queryQuestionRe.FindAllStringSubmatch(string(raw), -1) {
			out = append(out, m[1])
		}
	}
	return out
}

func readRelationPairs(path string) [][2]string {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	die(err)
	var out [][2]string
	for _, m := range relPairRe.FindAllStringSubmatch(string(raw), -1) {
		out = append(out, [2]string{m[1], m[2]})
	}
	return out
}
