package stats

// Какое правило отбора выдержки связи лучше (этап 104, П5.3). Стенд: ничего
// не меняет, граф и коллекцию только читает.
//
// **Что на деле видит человек.** Под связью печатается ОДНА выдержка — окно
// в 140 знаков вокруг ближайшей пары имён (`evidenceLine`, format.go). Кусок
// для неё берётся из первых `max_evidences` (4) записей связи в порядке
// хранения: первый, где есть оба имени, иначе просто первый. Замер 17.09.2026
// на честной выборке: сами эти четыре куска ничем не лучше остальных
// (оба имени видны у 66,2% против 66,0%), то есть выбор идёт из случайной
// четвёрки. Вопрос стенда — сколько даёт выбор из большего числа кусков
// и по лучшему признаку, и сколько чтений хранилища это стоит.
//
// **Мера — то, что попадает на экран:** есть ли в выбранном куске оба имени
// и помещаются ли оба в окно выдержки. Не «содержательность» — её без модели
// не измерить.
//
// Два слоя: связи, выбранные равномерно (у большинства одно подтверждение,
// выбирать не из чего — это потолок пользы), и связи, которые показывает
// настоящий поиск на двух наборах вопросов (они-то и стоят перед глазами).

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

const evidenceWindow = 140 // FormatOpts.RelationRunes по умолчанию

// chunkView — что стенд знает о куске-кандидате.
type chunkView struct {
	ok      bool
	service bool // оглавление или список литературы
	both    bool // оба имени есть в куске (та же проверка, что hasBoth)
	dist    int  // знаков между началами ближайшей пары имён; -1 — пары нет
	fits    bool // оба имени помещаются в окно выдержки
}

func viewChunk(c *kb.Collection, k graph.ChunkKey, a, b string) chunkView {
	ci, ok := c.ChunkByRef(k.Doc, k.Ord)
	if !ok {
		return chunkView{}
	}
	text := strings.ToLower(strings.Join(strings.Fields(ci.Text), " "))
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	v := chunkView{ok: true, service: ci.TOC || ci.Refs || kb.LooksLikeTOC(ci.Text), dist: -1}
	if a == "" || b == "" {
		return v
	}
	pa, pb := positions(text, a), positions(text, b)
	if len(pa) == 0 || len(pb) == 0 {
		return v
	}
	v.both = true
	for _, i := range pa {
		for _, j := range pb {
			lo, hi, tail := i, j, b
			if j < i {
				lo, hi, tail = j, i, a
			}
			d := utf8.RuneCountInString(text[lo:hi])
			if v.dist < 0 || d < v.dist {
				v.dist = d
				v.fits = d+utf8.RuneCountInString(tail) <= evidenceWindow
			}
		}
	}
	return v
}

func positions(text, w string) []int {
	var out []int
	for from := 0; ; {
		i := strings.Index(text[from:], w)
		if i < 0 {
			return out
		}
		out = append(out, from+i)
		from += i + len(w)
	}
}

// evidenceRule — правило выбора куска из кандидатов связи.
type evidenceRule struct {
	name  string
	limit int  // сколько первых записей смотреть; 0 — все
	near  bool // выбирать кусок с ближайшей парой имён, а не первый с обоими
}

var evidenceRules = []evidenceRule{
	{"как сейчас: первые 4, первый с обоими именами", 4, false},
	{"первые 8", 8, false},
	{"первые 16", 16, false},
	{"все записи связи", 0, false},
	{"первые 4, ближайшая пара имён", 4, true},
	{"первые 16, ближайшая пара имён", 16, true},
	{"все записи, ближайшая пара имён", 0, true},
}

type ruleStat struct {
	n, both, fits, service, reads int
	spent                         time.Duration
}

// apply выбирает кусок по правилу и возвращает его вид и число чтений.
func (r evidenceRule) apply(c *kb.Collection, keys []graph.ChunkKey, a, b string) (chunkView, int) {
	if r.limit > 0 && len(keys) > r.limit {
		keys = keys[:r.limit]
	}
	var first, toc, best chunkView
	reads := 0
	for _, k := range keys {
		v := viewChunk(c, k, a, b)
		reads++
		if !v.ok {
			continue
		}
		if v.service {
			if !toc.ok {
				toc = v
			}
			continue
		}
		if !first.ok {
			first = v
		}
		if !v.both {
			continue
		}
		if !r.near {
			return v, reads // первый кусок с обоими именами — как в выдаче
		}
		if !best.ok || v.dist < best.dist {
			best = v
		}
		if v.fits && v.dist <= 60 {
			break // ближе соседнего предложения искать незачем
		}
	}
	switch {
	case best.ok:
		return best, reads
	case first.ok:
		return first, reads
	}
	return toc, reads
}

// relationKeys — записи связи «откуда → куда» в порядке хранения, без
// отброшенных книг: ровно тот перечень, из которого выбирает `Graph.Search`.
func relationKeys(g *graph.Graph, from, to uint32) []graph.ChunkKey {
	var keys []graph.ChunkKey
	for _, e := range g.Edges().Of(from) {
		if e.Dst != to || g.Dropped().Dropped(e.Evidence.Doc) {
			continue
		}
		keys = append(keys, e.Evidence)
	}
	return keys
}

type relCase struct {
	a, b string
	keys []graph.ChunkKey
}

func runRules(c *kb.Collection, cases []relCase) []ruleStat {
	stats := make([]ruleStat, len(evidenceRules))
	for _, rc := range cases {
		for i, r := range evidenceRules {
			t0 := time.Now()
			v, reads := r.apply(c, rc.keys, rc.a, rc.b)
			stats[i].spent += time.Since(t0)
			stats[i].n++
			stats[i].reads += reads
			if v.both {
				stats[i].both++
			}
			if v.fits {
				stats[i].fits++
			}
			if v.service {
				stats[i].service++
			}
		}
	}
	return stats
}

func printRules(title string, cases []relCase, stats []ruleStat) {
	many := 0
	for _, rc := range cases {
		if len(rc.keys) > 4 {
			many++
		}
	}
	fmt.Printf("\n  %s: связей %d, из них с запасом подтверждений (>4) %d\n", title, len(cases), many)
	fmt.Printf("  %-50s %10s %12s %10s %9s %12s\n", "правило", "оба имени", "оба в окне", "служебный", "чтений", "мс на связь")
	for i, r := range evidenceRules {
		s := stats[i]
		if s.n == 0 {
			continue
		}
		pc := func(x int) string { return fmt.Sprintf("%.1f%%", 100*float64(x)/float64(s.n)) }
		fmt.Printf("  %-50s %10s %12s %10s %9.1f %12.3f\n", r.name, pc(s.both), pc(s.fits), pc(s.service),
			float64(s.reads)/float64(s.n), float64(s.spent.Microseconds())/1000/float64(s.n))
	}
}

func evidenceRule104(cfg *config.Config, g *graph.Graph, c *kb.Collection, sample int, seed int64, sets []string) {
	fmt.Printf("\nП5.3. Правило отбора выдержки связи (окно выдержки %d знаков, зерно %d)\n", evidenceWindow, seed)

	// Слой 1: равномерно по направленным связям.
	all := directedPairs(g)
	rnd := rand.New(rand.NewSource(seed))
	rnd.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	var uniform, rich []relCase
	for _, p := range all {
		if len(uniform) >= sample && len(rich) >= sample {
			break
		}
		src, ok1 := g.Entities().Get(p[0])
		dst, ok2 := g.Entities().Get(p[1])
		if !ok1 || !ok2 {
			continue
		}
		keys := relationKeys(g, p[0], p[1])
		if len(keys) == 0 {
			continue
		}
		rc := relCase{src.Name, dst.Name, keys}
		if len(uniform) < sample {
			uniform = append(uniform, rc)
		}
		if len(keys) > 4 && len(rich) < sample {
			rich = append(rich, rc)
		}
	}
	printRules("все связи графа, равномерно", uniform, runRules(c, uniform))
	printRules("только связи с запасом подтверждений", rich, runRules(c, rich))

	// Слой 2: связи, которые показывает настоящий поиск.
	deps := find.Deps{Coll: c, Graph: g,
		Embedder: kbembed.New(cfg.KB.EmbedOptions(), cfg.EmbedFallback(), 2*time.Minute, nil)}
	for _, set := range sets {
		var shown []relCase
		wordsOnly := 0
		for _, q := range readMethodQuestions(set) {
			res, err := find.Search(context.Background(), deps, q, find.Opts{})
			if err != nil {
				continue
			}
			if res.WordsOnly {
				wordsOnly++
			}
			for _, rel := range res.Relations {
				a, ok1 := g.Entities().Lookup(rel.Src)
				b, ok2 := g.Entities().Lookup(rel.Dst)
				if !ok1 || !ok2 {
					continue
				}
				if keys := relationKeys(g, a.ID, b.ID); len(keys) > 0 {
					shown = append(shown, relCase{rel.Src, rel.Dst, keys})
				}
			}
		}
		printRules("связи в выдаче поиска, набор "+set, shown, runRules(c, shown))
		if wordsOnly > 0 {
			fmt.Printf("  ВНИМАНИЕ: у %d вопросов смысловой вход не отработал\n", wordsOnly)
		}
	}
	// Слой 3: то же, но через НАСТОЯЩУЮ отрисовку выдачи (`graph.Render`) —
	// копия правила в стенде могла разойтись с кодом. Сравнение одним бинарём:
	// выключатель RenderOpts.EvidenceFirst возвращает прежний выбор.
	fmt.Printf("\n  сквозная проверка через graph.Render (кандидатов на связь: %d)\n", g.Rules().MaxEvidences)
	fmt.Printf("  %-44s %8s %22s %22s\n", "набор", "связей", "оба имени: как было", "оба имени: ближайшая пара")
	for _, set := range sets {
		var rels, oldOK, newOK int
		for _, q := range readMethodQuestions(set) {
			res, err := find.Search(context.Background(), deps, q, find.Opts{})
			if err != nil {
				continue
			}
			sr := graph.SearchResult{Entities: res.Entities, Relations: res.Relations}
			was := excerptsWithBoth(graph.Render(c, sr, graph.RenderOpts{MaxRelations: 1000, EvidenceFirst: true}), res.Relations)
			now := excerptsWithBoth(graph.Render(c, sr, graph.RenderOpts{MaxRelations: 1000}), res.Relations)
			rels += len(res.Relations)
			oldOK += was
			newOK += now
		}
		if rels > 0 {
			fmt.Printf("  %-44s %8d %15d (%4.1f%%) %15d (%4.1f%%)\n", cut(set, 44), rels,
				oldOK, 100*float64(oldOK)/float64(rels), newOK, 100*float64(newOK)/float64(rels))
		}
	}

	fmt.Println()
	fmt.Println("  «Оба в окне» — оба имени связи помещаются в печатаемые 140 знаков: это и есть")
	fmt.Println("  выдержка, по которой человек видит связь. «Чтений» — кусков хранилища на связь.")
}

// excerptsWithBoth считает в отрисованной выдаче связи, под которыми напечатана
// выдержка с ОБОИМИ именами. Связи идут в тексте в том же порядке, что в списке:
// строка связи начинается с двух пробелов и имени, выдержка — строкой ниже
// с шестью пробелами.
func excerptsWithBoth(text string, rels []graph.FoundRelation) int {
	lines := strings.Split(text, "\n")
	n, at := 0, 0
	for at < len(lines) && !strings.HasPrefix(lines[at], "Связи:") {
		at++ // карточки понятий стоят выше и тоже начинаются с имени
	}
	for _, r := range rels {
		head := "  " + r.Src + " —"
		for at < len(lines) && !strings.HasPrefix(lines[at], head) {
			at++
		}
		if at+1 >= len(lines) {
			break
		}
		at++
		if !strings.HasPrefix(lines[at], "      ") {
			continue
		}
		low := strings.ToLower(lines[at])
		if strings.Contains(low, strings.ToLower(r.Src)) && strings.Contains(low, strings.ToLower(r.Dst)) {
			n++
		}
	}
	return n
}
