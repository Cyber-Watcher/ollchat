package stats

// Покрытие типов связей (паспорт опытного графа, решение 13; мера 9 паспорта
// сравнения). Без карты, граф только читается.
//
// «Coverage verifies that all critical relationship types are captured. Your
// ontology might define 15 relationship types, but if only 8 appear…» — схема
// извлечения задаёт типы, а какие из них на деле встречаются и сколько связей
// ушло в безымянное «связано», не мерилось ни разу. Для сравнения двух графов
// это прямая мера: формат 2 запрещает «связано», и разница должна быть видна
// числом — и по всему графу, и по каталогу сравнения.

import (
	"fmt"
	"sort"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

func relTypes(g *graph.Graph, c *kb.Collection, folder string) {
	inFolder := map[uint32]bool{}
	roots := c.Roots()
	for _, b := range c.MatchingDocs(kb.ChunkFilter{}) {
		if folderOf(b.Path, roots) == folder {
			inFolder[b.ID] = true
		}
	}
	all := map[uint8]int{}
	part := map[uint8]int{}
	var total, totalPart int
	for _, e := range g.Entities().Live() {
		for _, ed := range g.Edges().Of(e.ID) {
			all[ed.Type]++
			total++
			if inFolder[ed.Evidence.Doc] {
				part[ed.Type]++
				totalPart++
			}
		}
	}
	types := make([]uint8, 0, len(all))
	for t := range all {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool { return all[types[i]] > all[types[j]] })

	fmt.Printf("\nРешение 13. Покрытие типов связей: записей связей %d, из них по книгам %s — %d (книг %d)\n\n",
		total, folder, totalPart, len(inFolder))
	fmt.Printf("  %-22s %12s %8s %14s %8s\n", "тип", "весь граф", "доля", folder, "доля")
	named, namedPart := 0, 0
	for _, t := range types {
		if t != graph.RelRelated {
			named += all[t]
			namedPart += part[t]
		}
		fmt.Printf("  %-22s %12d %7.1f%% %14d %7.1f%%\n", graph.RelName(t), all[t],
			100*float64(all[t])/float64(max(total, 1)), part[t], 100*float64(part[t])/float64(max(totalPart, 1)))
	}
	fmt.Printf("\n  типов встречается: %d; названное отношение у %.1f%% записей (по %s — %.1f%%),\n",
		len(types), 100*float64(named)/float64(max(total, 1)), folder, 100*float64(namedPart)/float64(max(totalPart, 1)))
	fmt.Println("  остальное — «связано»: в формате 2 такие связи не пишутся вовсе.")
}
