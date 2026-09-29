package stats

// Возраст знания в графе: каким годом подтверждена связь (этап 104, П10).
//
// **Откуда мысль.** «Building AI Agents with LLMs, RAG, and Knowledge Graphs»
// (Raieli, Iuculano, 2025, стр. 250) перечисляет измерения качества графа,
// и среди них — timeliness:
//
//   > Knowledge should also be updated regularly because it can change and
//   > become outdated. Therefore, it is important to decide the frequency
//   > of updates.
//
//   > Знание нужно обновлять: оно меняется и устаревает. Поэтому важно решить,
//   > с какой частотой обновлять.
//
// У нас год издания книги **показывается** рядом с выдержкой (`format.go`),
// но не участвует ни в весе связи, ни в отборе. Связь, подтверждённая только
// книгой 2021 года, весит столько же, сколько подтверждённая книгой 2026-го, —
// а в технической библиотеке за пять лет меняются версии, имена инструментов
// и сами «лучшие практики».
//
// **Что здесь считается.** По выборке связей: год самой свежей книги,
// подтверждающей связь, и год самой старой. Отдельно — доля связей, у которых
// ВСЕ подтверждения старше заданного года.
//
// Ничего не меняет: читает граф и печатает числа.

import (
	"fmt"
	"sort"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

func ageCheck(g *graph.Graph, c *kb.Collection, sample int, old int, seed int64) {
	yearOf := map[uint32]int{}
	for _, b := range c.MatchingDocs(kb.ChunkFilter{}) {
		yearOf[b.ID] = b.Year
	}
	known := 0
	for _, y := range yearOf {
		if y > 0 {
			known++
		}
	}
	fmt.Printf("\nП10. Возраст знания: книг всего %d, год известен у %d\n",
		len(yearOf), known)
	if known == 0 {
		fmt.Println("  годы книг не проставлены — сперва ollchat --kb-years")
		return
	}

	byNewest := map[int]int{}
	var checked, noYear, onlyOld int

	// Выборка равномерна по парам связанных понятий (graph.SamplePairs):
	// прежняя «понятие → его связь» давала доли по понятиям (аудит 17.09, S9).
	for _, pr := range g.SamplePairs(sample, seed) {
		ed := graph.Edge{Src: pr[0], Dst: pr[1]}
		// Все подтверждения этой пары, а не одно: связь свежа настолько,
		// насколько свежа самая новая книга, её подтвердившая.
		newest, seen := 0, false
		for _, x := range g.Edges().Between(ed.Src, ed.Dst) {
			if y := yearOf[x.Evidence.Doc]; y > 0 {
				seen = true
				if y > newest {
					newest = y
				}
			}
		}
		checked++
		if !seen {
			noYear++
			continue
		}
		byNewest[newest]++
		if newest < old {
			onlyOld++
		}
	}

	fmt.Printf("  проверено связей %d (выборка, зерно %d)\n\n", checked, seed)
	years := make([]int, 0, len(byNewest))
	for y := range byNewest {
		years = append(years, y)
	}
	sort.Ints(years)
	fmt.Println("  год самой свежей подтверждающей книги:")
	for _, y := range years {
		n := byNewest[y]
		fmt.Printf("    %d  %5d  %s\n", y, n, bar(100*float64(n)/float64(checked)))
	}
	fmt.Printf("\n  год не известен ни у одного подтверждения: %d (%.1f%%)\n",
		noYear, 100*float64(noYear)/float64(checked))
	fmt.Printf("  ВСЕ подтверждения старше %d года: %d (%.1f%%)\n",
		old, onlyOld, 100*float64(onlyOld)/float64(checked))
	fmt.Println()
	fmt.Println("  Это не повод выбрасывать старые связи: «Go использует горутины»")
	fmt.Println("  не устаревает. Но версия, имя инструмента или «как принято»")
	fmt.Println("  за пять лет меняются, и человеку полезно видеть, чем именно")
	fmt.Println("  подтверждена связь — годом, а не только числом подтверждений.")
}

// bar — простая полоска для глаза, доля в процентах.
func bar(pct float64) string {
	n := int(pct / 2)
	if n > 40 {
		n = 40
	}
	s := ""
	for i := 0; i < n; i++ {
		s += "▌"
	}
	return fmt.Sprintf("%-40s %.1f%%", s, pct)
}
