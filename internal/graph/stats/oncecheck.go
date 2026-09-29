package stats

// Чем связи с ОДНИМ подтверждением отличаются от многократных (этап 104, П1.1).
//
// **Зачем.** 713 681 связь из 852 928 (83%) держится на одной фразе из одной
// книги — и весит в выдаче столько же, сколько подтверждённая сорока книгами.
// Считалось само собой разумеющимся, что такие связи хуже. **Не проверялось
// ни разу.**
//
// Порог веса ребра уже отвергнут замером 09.09.2026: он оставил бы 143 тысячи
// понятий вовсе без связей. Речь не о нём, а о том, чтобы знать, стоит ли
// вообще что-то делать: если по качеству две половины неразличимы, то 83% —
// не проблема, а свойство книжного графа, и пункт закрывается отрицательным
// результатом.
//
// **Три меры, все считаются на готовом графе:**
//   1. подтверждается ли связь буквально (оба имени в куске) — как в -provenance;
//   2. из своей ли области подтверждение (каталог книги против области понятия);
//   3. год книги — не держатся ли одиночные связи на старье.
//
// Ничего не меняет.

import (
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// onceGroup — счётчики одной половины (одно подтверждение / несколько).
type onceGroup struct {
	n        int
	both     int // оба имени связи есть в куске
	none     int // ни одного имени
	alien    int // подтверждение из чужого каталога
	yearSum  int
	yearKnow int
	oldOnly  int // подтверждение старше 2023 года
}

func (g onceGroup) pct(x int) string {
	if g.n == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(x)/float64(g.n))
}

func (g onceGroup) meanYear() string {
	if g.yearKnow == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f", float64(g.yearSum)/float64(g.yearKnow))
}

func onceCheck(g *graph.Graph, c *kb.Collection, sample int, seed int64) {
	byDoc := map[uint32]string{}
	yearOf := map[uint32]int{}
	roots := c.Roots()
	for _, b := range c.MatchingDocs(kb.ChunkFilter{}) {
		byDoc[b.ID] = folderOf(b.Path, roots)
		yearOf[b.ID] = b.Year
	}
	// Область понятия — преобладающий каталог его упоминаний.
	folderCache := map[uint32]string{}
	folderOfEntity := func(id uint32) string {
		if f, ok := folderCache[id]; ok {
			return f
		}
		best := mainFolder(g, byDoc, id)
		folderCache[id] = best
		return best
	}

	var once, many onceGroup

	// Выборка равномерна по парам связанных понятий; «одно подтверждение» —
	// по числу записей пары в ОБЕ стороны, а не по счётчику одного направления
	// (аудит 17.09.2026, S9 и M16).
	for _, pr := range g.SamplePairs(sample, seed) {
		e, ok := g.Entities().Get(pr[0])
		if !ok {
			continue
		}
		dst, ok := g.Entities().Get(pr[1])
		if !ok {
			continue
		}
		edges := g.Edges().Between(e.ID, dst.ID)
		if len(edges) == 0 {
			continue
		}
		grp := &many
		if len(edges) <= 1 {
			grp = &once
		}
		grp.n++

		ed := edges[0]
		if y := yearOf[ed.Evidence.Doc]; y > 0 {
			grp.yearSum += y
			grp.yearKnow++
			if y < 2023 {
				grp.oldOnly++
			}
		}
		if byDoc[ed.Evidence.Doc] != folderOfEntity(e.ID) {
			grp.alien++
		}
		ci, ok := c.ChunkByRef(ed.Evidence.Doc, ed.Evidence.Ord)
		if !ok {
			continue
		}
		low := strings.ToLower(ci.Text)
		seen := func(ent graph.Entity) bool {
			if strings.Contains(low, strings.ToLower(ent.Name)) {
				return true
			}
			for _, al := range g.Entities().DisplayAliases(ent) {
				if len([]rune(al)) >= 3 && strings.Contains(low, strings.ToLower(al)) {
					return true
				}
			}
			return false
		}
		a, b := seen(e), seen(dst)
		switch {
		case a && b:
			grp.both++
		case !a && !b:
			grp.none++
		}
	}

	fmt.Printf("\nП1.1. Связи с одним подтверждением против многократных (выборка, зерно %d)\n\n", seed)
	fmt.Printf("  %-34s %12s %12s\n", "", "одно", "несколько")
	fmt.Printf("  %-34s %12d %12d\n", "связей в выборке", once.n, many.n)
	fmt.Printf("  %-34s %12s %12s\n", "оба имени видны в куске", once.pct(once.both), many.pct(many.both))
	fmt.Printf("  %-34s %12s %12s\n", "ни одного имени (вероятно, выдумка)", once.pct(once.none), many.pct(many.none))
	fmt.Printf("  %-34s %12s %12s\n", "подтверждение из чужой области", once.pct(once.alien), many.pct(many.alien))
	fmt.Printf("  %-34s %12s %12s\n", "средний год подтверждения", once.meanYear(), many.meanYear())
	fmt.Printf("  %-34s %12s %12s\n", "подтверждение старше 2023", once.pct(once.oldOnly), many.pct(many.oldOnly))
	fmt.Println()
	fmt.Println("  Если доли близки — 83% одиночных связей не беда, а свойство")
	fmt.Println("  книжного графа: книга говорит о паре понятий один раз, и этого")
	fmt.Println("  довольно. Тогда П1 закрывается отрицательным результатом,")
	fmt.Println("  и трогать ранжирование незачем.")
}

// mainFolder — преобладающий каталог понятия по книгам, где оно встречается.
// При равном счёте берётся меньшее по алфавиту имя: обход карты в Go случаен,
// и без этого правила два прогона одного замера давали разные числа
// (аудит 17.09.2026).
func mainFolder(g *graph.Graph, byDoc map[uint32]string, id uint32) string {
	count := map[string]int{}
	for _, k := range g.Mentions().Of(id) {
		count[byDoc[k.Doc]]++
	}
	best, n := "", 0
	for f, c := range count {
		if c > n || (c == n && f < best) {
			best, n = f, c
		}
	}
	return best
}
