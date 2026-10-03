package stats

// Попадает ли в показанные выдержки самое содержательное (этап 104, П5.2).
//
// **Зачем.** У связи бывает 186 подтверждений, показывается до четырёх
// (`max_evidences`). Отбор идёт по наличию обоих имён в куске — но не по тому,
// объясняет ли кусок связь. Проверяем, лучше ли показанные куски, чем
// случайные из того же множества.
//
// **Мера — не «содержательность» (её без модели не измерить), а признаки,
// которые с ней связаны:** оба ли имени в куске, стоят ли они рядом
// (в пределах 200 знаков), не служебный ли это кусок (оглавление, список
// литературы), длина текста.
//
// Ничего не меняет.

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

type pickStat struct {
	n, both, near, service int
	lenSum                 int
}

func (p pickStat) pct(x int) string {
	if p.n == 0 {
		return "—"
	}
	return fmt.Sprintf("%5.1f%%", 100*float64(x)/float64(p.n))
}

func (p pickStat) meanLen() string {
	if p.n == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f", float64(p.lenSum)/float64(p.n))
}

// look разбирает один кусок: видны ли оба имени, рядом ли они, служебный ли он.
func look(ci kb.ChunkInfo, a, b string) (both, near, service bool) {
	low := strings.ToLower(ci.Text)
	ia := strings.Index(low, strings.ToLower(a))
	ib := strings.Index(low, strings.ToLower(b))
	both = ia >= 0 && ib >= 0
	if both {
		// Расстояние в знаках, не в байтах: в русском тексте байтов вдвое
		// больше, и «до 200 знаков» на деле было около ста.
		lo, hi := min(ia, ib), max(ia, ib)
		near = utf8.RuneCountInString(low[lo:hi]) <= 200
	}
	service = ci.TOC || ci.Refs
	return
}

// directedPairs — все направленные связи «откуда → куда» живых понятий
// по порядку номеров. Именно такими их собирает выдача (`Graph.Search`):
// исходящие записи понятия, у которых совпал конец, в порядке хранения.
func directedPairs(g *graph.Graph) [][2]uint32 {
	var all [][2]uint32
	for _, e := range g.Entities().Live() {
		seen := map[uint32]bool{}
		var dsts []uint32
		for _, ed := range g.Edges().Of(e.ID) {
			if !seen[ed.Dst] {
				seen[ed.Dst] = true
				dsts = append(dsts, ed.Dst)
			}
		}
		sort.Slice(dsts, func(i, j int) bool { return dsts[i] < dsts[j] })
		for _, d := range dsts {
			all = append(all, [2]uint32{e.ID, d})
		}
	}
	return all
}

func evidencePick(g *graph.Graph, c *kb.Collection, sample, shown int, seed int64, old bool) {
	rnd := rand.New(rand.NewSource(seed))
	var picked, other pickStat
	checked := 0
	how := "равномерно по направленным связям, отбор как в выдаче"

	// count разбирает одну связь: первые shown подтверждений против остальных.
	count := func(src, dst graph.Entity, edges []graph.Edge) {
		if len(edges) <= shown {
			return // показаны все, выбора не было
		}
		checked++
		for i, ed := range edges {
			ci, ok := c.ChunkByRef(ed.Evidence.Doc, ed.Evidence.Ord)
			if !ok {
				continue
			}
			grp := &other
			if i < shown { // показываются первые: порядок хранения
				grp = &picked
			}
			both, near, service := look(ci, src.Name, dst.Name)
			grp.n++
			grp.lenSum += len([]rune(ci.Text))
			if both {
				grp.both++
			}
			if near {
				grp.near++
			}
			if service {
				grp.service++
			}
		}
	}

	if old {
		// ПРЕЖНИЙ способ, для сравнения одним бинарём: понятие, затем сосед,
		// подтверждения обеих сторон вперемешку (`Between`). Связь хаба
		// попадала в выборку в сотни раз реже связи одиночки, а порядок
		// «показанных» не совпадал с настоящим порядком выдачи.
		how = "ПРЕЖНИЙ способ: понятие, затем сосед; обе стороны связи"
		live := g.Entities().Live()
		for tries := 0; checked < sample && tries < sample*30; tries++ {
			e := live[rnd.Intn(len(live))]
			nb := g.Edges().Neighbors(e.ID)
			if len(nb) == 0 {
				continue
			}
			n := nb[rnd.Intn(len(nb))]
			dst, ok := g.Entities().Get(n.ID)
			if !ok {
				continue
			}
			count(e, dst, g.Edges().Between(e.ID, n.ID))
		}
	} else {
		all := directedPairs(g)
		rnd.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
		for _, p := range all {
			if checked >= sample {
				break
			}
			src, ok1 := g.Entities().Get(p[0])
			dst, ok2 := g.Entities().Get(p[1])
			if !ok1 || !ok2 {
				continue
			}
			var edges []graph.Edge
			for _, ed := range g.Edges().Of(p[0]) {
				// Подтверждения из отброшенных книг выдача не показывает
				// (search.go) — и здесь они в «показанные» не идут.
				if ed.Dst == p[1] && !g.Dropped().Dropped(ed.Evidence.Doc) {
					edges = append(edges, ed)
				}
			}
			count(src, dst, edges)
		}
	}

	fmt.Printf("\nП5.2. Что попадает в показанные выдержки (связей с запасом: %d, зерно %d; выборка: %s)\n\n", checked, seed, how)
	fmt.Printf("  %-34s %12s %12s\n", "", "показанные", "остальные")
	fmt.Printf("  %-34s %12d %12d\n", "кусков рассмотрено", picked.n, other.n)
	fmt.Printf("  %-34s %12s %12s\n", "оба имени связи видны", picked.pct(picked.both), other.pct(other.both))
	fmt.Printf("  %-34s %12s %12s\n", "имена рядом (до 200 знаков)", picked.pct(picked.near), other.pct(other.near))
	fmt.Printf("  %-34s %12s %12s\n", "служебный кусок (оглавление/ссылки)", picked.pct(picked.service), other.pct(other.service))
	fmt.Printf("  %-34s %12s %12s\n", "длина куска, знаков", picked.meanLen(), other.meanLen())
	fmt.Println()
	fmt.Println("  Если показанные не лучше остальных — отбор выдержек не работает,")
	fmt.Println("  и человеку достаётся случайный кусок из множества подтверждений.")
}
