package stats

// Стареет ли КАЧЕСТВО связей вместе с их возрастом (этап 104, П10.2).
//
// **Зачем.** Замер `-agecheck` 16.09.2026 показал: 16,3% связей держатся
// только на книгах старше 2023 года. Осталось понять, хуже ли они — или
// разница лишь в содержании, а не в добротности. Если качество одинаково,
// возраст остаётся вопросом ПОКАЗА (П10.1 сделано), и трогать веса незачем.
//
// Меры те же, что в -oncecheck, чтобы числа были сравнимы:
// видно ли связь в её куске, из своей ли области подтверждение,
// сколько у связи подтверждений.
//
// Ничего не меняет.

import (
	"fmt"
	"math"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

type ageGroup struct {
	n, both, none, alien, once int
	countSum                   int
}

func (a ageGroup) pct(x int) string {
	if a.n == 0 {
		return "—"
	}
	return fmt.Sprintf("%5.1f%%", 100*float64(x)/float64(a.n))
}

func (a ageGroup) meanCount() string {
	if a.n == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f", float64(a.countSum)/float64(a.n))
}

// zTest — разница долей значима или в пределах шума. Считается здесь, чтобы
// вывод не приходилось подкреплять глазомером: 16.09.2026 «0 из 15» выглядели
// сильным сигналом и растворились на 121 наблюдении.
func zTest(a, na, b, nb int) string {
	if na == 0 || nb == 0 {
		return "—"
	}
	pa, pb := float64(a)/float64(na), float64(b)/float64(nb)
	p := float64(a+b) / float64(na+nb)
	se := math.Sqrt(p * (1 - p) * (1/float64(na) + 1/float64(nb)))
	if se == 0 {
		return "—"
	}
	z := (pa - pb) / se
	verdict := "не значимо"
	if math.Abs(z) > 1.96 {
		verdict = "ЗНАЧИМО"
	}
	return fmt.Sprintf("z = %5.2f  %s", z, verdict)
}

func ageQuality(g *graph.Graph, c *kb.Collection, sample, border int, seed int64) {
	byDoc := map[uint32]string{}
	yearOf := map[uint32]int{}
	roots := c.Roots()
	for _, b := range c.MatchingDocs(kb.ChunkFilter{}) {
		byDoc[b.ID] = folderOf(b.Path, roots)
		yearOf[b.ID] = b.Year
	}
	folderCache := map[uint32]string{}
	folderOfEntity := func(id uint32) string {
		if f, ok := folderCache[id]; ok {
			return f
		}
		best := mainFolder(g, byDoc, id)
		folderCache[id] = best
		return best
	}

	var old, fresh ageGroup

	// Выборка равномерна по парам связанных понятий (аудит 17.09.2026, S9).
	for _, pr := range g.SamplePairs(sample, seed) {
		e, ok := g.Entities().Get(pr[0])
		if !ok {
			continue
		}
		dst, ok := g.Entities().Get(pr[1])
		if !ok {
			continue
		}
		n := graph.Neighbor{ID: dst.ID, Count: len(g.Edges().Between(e.ID, dst.ID))}
		// Возраст связи — год самой СВЕЖЕЙ книги, её подтвердившей:
		// связь жива настолько, насколько свежее лучшее подтверждение.
		newest, ed, has := 0, graph.Edge{}, false
		for _, x := range g.Edges().Between(e.ID, n.ID) {
			if y := yearOf[x.Evidence.Doc]; y > newest {
				newest, ed, has = y, x, true
			}
		}
		if !has {
			continue // год не известен — в сравнении не участвует
		}
		grp := &fresh
		if newest < border {
			grp = &old
		}
		grp.n++
		grp.countSum += n.Count
		if n.Count <= 1 {
			grp.once++
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

	fmt.Printf("\nП10.2. Стареет ли КАЧЕСТВО связей (граница %d года, зерно %d)\n\n", border, seed)
	fmt.Printf("  %-32s %10s %10s   %s\n", "", "старые", "свежие", "значимость")
	fmt.Printf("  %-32s %10d %10d\n", "связей в выборке", old.n, fresh.n)
	fmt.Printf("  %-32s %10s %10s   %s\n", "оба имени видны в куске",
		old.pct(old.both), fresh.pct(fresh.both), zTest(old.both, old.n, fresh.both, fresh.n))
	fmt.Printf("  %-32s %10s %10s   %s\n", "ни одного имени (выдумка)",
		old.pct(old.none), fresh.pct(fresh.none), zTest(old.none, old.n, fresh.none, fresh.n))
	fmt.Printf("  %-32s %10s %10s   %s\n", "подтверждение из чужой области",
		old.pct(old.alien), fresh.pct(fresh.alien), zTest(old.alien, old.n, fresh.alien, fresh.n))
	fmt.Printf("  %-32s %10s %10s   %s\n", "одно подтверждение",
		old.pct(old.once), fresh.pct(fresh.once), zTest(old.once, old.n, fresh.once, fresh.n))
	fmt.Printf("  %-32s %10s %10s\n", "подтверждений в среднем", old.meanCount(), fresh.meanCount())
	fmt.Println()
	fmt.Println("  Если разницы нет — возраст остаётся вопросом ПОКАЗА (сделано,")
	fmt.Println("  П10.1), и трогать веса связей незачем: старая книга не хуже,")
	fmt.Println("  она про другое время.")
}
