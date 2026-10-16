package stats

// Выборка связей одного типа с выдержкой — для разметки точности типа глазами
// (этап 112, О3: «точность типа на случайной выборке по 100 рёбер на тип, первыми
// „противопоставлено“ и „является“»). Связи берутся равномерно по записям
// (graph.SampleEdges, как у -provenance), выдержка — окно, где оба конца стоят
// ближе всего (graph.EvidenceWindow, то же правило, что у выдачи). Карта не нужна.

import (
	"fmt"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// typeSampleRunes — длина окна выдержки для разметки.
const typeSampleRunes = 360

func typeSample(g *graph.Graph, c *kb.Collection, typ string, n int, seed int64) {
	// Запас выборки: самый редкий тип («пример») — около 2 % связей.
	pool := g.SampleEdges(n*80, seed)
	var got, seen int
	for _, ed := range pool {
		if got >= n {
			break
		}
		if graph.RelName(ed.Type) != typ {
			continue
		}
		seen++
		src, ok := g.Entities().Get(ed.Src)
		if !ok {
			continue
		}
		dst, ok := g.Entities().Get(ed.Dst)
		if !ok {
			continue
		}
		if g.Dropped().Dropped(ed.Evidence.Doc) {
			continue
		}
		ci, ok := c.ChunkByRef(ed.Evidence.Doc, ed.Evidence.Ord)
		if !ok {
			continue
		}
		got++
		fmt.Printf("%d. %s —%s→ %s (%s)\n   %s\n\n", got, src.Name, typ, dst.Name, ed.Evidence.String(),
			oneLine(graph.EvidenceWindow(ci.Text, src.Name, dst.Name, typeSampleRunes)))
	}
	fmt.Printf("тип «%s»: показано %d из %d найденных в выборке %d связей (зерно %d)\n", typ, got, seen, len(pool), seed)
}
