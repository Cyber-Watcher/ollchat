package stats

// Что в графе держится ТОЛЬКО на старых книгах (этап 104, П10.3): перепись
// для набора вопросов «про версии и инструменты». Ничего не меняет.
//
// Набор должен бить туда, где пометка года действительно появится в карте
// понятий, а не в наши представления об устаревшем. Поэтому кандидаты берутся
// из графа: связи технологий и инструментов с заметным числом подтверждений,
// у которых самая свежая подтверждающая книга не новее заданного года. Что из
// этого действительно устарело по существу, решает человек — глазами по списку.

import (
	"fmt"
	"sort"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

func staleRels(g *graph.Graph, c *kb.Collection, maxYear, minCount, show int) {
	yearOf := map[uint32]int{}
	for _, b := range c.MatchingDocs(kb.ChunkFilter{}) {
		yearOf[b.ID] = b.Year
	}
	type row struct {
		src, dst, typ string
		count, books  int
		newest, first int
		srcMentions   int
	}
	var rows []row
	for _, e := range g.Entities().Live() {
		if e.Type != graph.TypeTech {
			continue
		}
		byDst := map[uint32][]graph.Edge{}
		for _, ed := range g.Edges().Of(e.ID) {
			byDst[ed.Dst] = append(byDst[ed.Dst], ed)
		}
		for dst, list := range byDst {
			if len(list) < minCount {
				continue
			}
			newest, first := 0, 9999
			books := map[uint32]bool{}
			typ := map[uint8]int{}
			for _, ed := range list {
				books[ed.Evidence.Doc] = true
				typ[ed.Type]++
				if y := yearOf[ed.Evidence.Doc]; y > 0 {
					if y > newest {
						newest = y
					}
					if y < first {
						first = y
					}
				}
			}
			if newest == 0 || newest > maxYear || len(books) < 2 {
				continue
			}
			d, ok := g.Entities().Get(dst)
			if !ok {
				continue
			}
			best, bestN := uint8(0), 0
			for t, n := range typ {
				if n > bestN {
					best, bestN = t, n
				}
			}
			rows = append(rows, row{e.Name, d.Name, graph.RelName(best), len(list), len(books), newest, first,
				len(g.Mentions().Of(e.ID))})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].src+rows[i].dst < rows[j].src+rows[j].dst
	})
	fmt.Printf("\nП10.3. Связи технологий, подтверждённые только книгами не новее %d года (подтверждений ≥ %d, книг ≥ 2): %d\n\n",
		maxYear, minCount, len(rows))
	fmt.Printf("  %-5s %-5s %-9s %-30s %-16s %s\n", "подтв", "книг", "годы", "откуда", "тип", "куда")
	for i, r := range rows {
		if i >= show {
			fmt.Printf("  …и ещё %d\n", len(rows)-show)
			break
		}
		fmt.Printf("  %-5d %-5d %d–%d %-30s %-16s %s\n", r.count, r.books, r.first, r.newest,
			cut(r.src, 30), r.typ, cut(r.dst, 40))
	}
}
