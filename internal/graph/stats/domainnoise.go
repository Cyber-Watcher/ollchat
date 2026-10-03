package stats

// Шум чужой области в выдаче графа — находка Н10 (15.09.2026).
//
// Запрос про RAG и графы приносит связи из книг по Kubernetes и Docker: понятие
// `nodes` (935 упоминаний в 143 книгах) — это и вершины графа, и узлы кластера.
// Книги советуют фильтр по метаданным на первом этапе отбора («Hands-On RAG for
// Production», 2026, стр. 101), причём в коде, а не моделью.
//
// **Этот замер — до правки, а не вместо неё.** Надо знать, какая доля связей
// в окружении понятий из нашей области подтверждена книгами ЧУЖИХ каталогов.
// Без числа непонятно, лечим ли мы болезнь или впечатление от двух запросов.
//
// Карта не нужна, граф только на чтение.

import (
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// folderOf — каталог библиотеки по пути книги: «/AI», «/DevOps», «(корень)».
func folderOf(path string, roots []string) string {
	p := path
	// Самый короткий подходящий корень: у коллекции lab корень — сам
	// <корень библиотеки>/Раздел, и по нему каталог книги вырождался в «(корень)»
	// (20.09.2026: 100 % «чужих» у lab). Корни библиотеки (kb.roots) короче
	// корней коллекции — их и надо резать.
	best := ""
	for _, r := range roots {
		if r != "" && strings.HasPrefix(p, r) && (best == "" || len(r) < len(best)) {
			best = r
		}
	}
	p = strings.TrimPrefix(p, best)
	p = strings.TrimPrefix(p, "/")
	if i := strings.Index(p, "/"); i > 0 {
		return "/" + p[:i]
	}
	return "(корень)"
}

// domainNoise считает, из каких каталогов приходят подтверждения связей
// у названных понятий.
func domainNoise(g *graph.Graph, c *kb.Collection, names []string, want string) {
	roots := c.Roots()
	byDoc := map[uint32]string{}
	for _, b := range c.MatchingDocs(kb.ChunkFilter{}) {
		byDoc[b.ID] = folderOf(b.Path, roots)
	}

	fmt.Printf("\nН10. Откуда подтверждения связей (этап 103; своя область — %s)\n", want)
	fmt.Printf("  книг в коллекции: %d, каталогов: %d\n", len(byDoc), countFolders(byDoc))
	fmt.Println()
	fmt.Println("  понятие                      связей   свои   чужие   ТОЛЬКО чужие")

	totalEdges, totalAlien, totalOnlyAlien := 0, 0, 0
	for _, name := range names {
		ent, ok := g.Entities().Lookup(name)
		if !ok {
			fmt.Printf("  %-28s — нет в графе\n", cut(name, 28))
			continue
		}
		edges := edgesAround(g, ent.ID)
		// Пара понятий может подтверждаться многими кусками: считаем по парам,
		// иначе одна книга с десятком упоминаний перевесит десять книг с одним.
		type pk struct{ a, b uint32 }
		own, alien := map[pk]int{}, map[pk]int{}
		for _, ed := range edges {
			k := pk{ed.Src, ed.Dst}
			if k.a > k.b {
				k.a, k.b = k.b, k.a
			}
			if byDoc[ed.Evidence.Doc] == want {
				own[k]++
			} else {
				alien[k]++
			}
		}
		pairs := map[pk]bool{}
		for k := range own {
			pairs[k] = true
		}
		for k := range alien {
			pairs[k] = true
		}
		onlyAlien := 0
		for k := range pairs {
			if own[k] == 0 && alien[k] > 0 {
				onlyAlien++
			}
		}
		fmt.Printf("  %-28s %6d %6d %7d %9d (%.0f%%)\n", cut(name, 28), len(pairs),
			len(own), len(alien), onlyAlien, 100*float64(onlyAlien)/float64(max(len(pairs), 1)))
		totalEdges += len(pairs)
		totalAlien += len(alien)
		totalOnlyAlien += onlyAlien
	}

	if totalEdges == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("  ИТОГО пар: %d, из них ТОЛЬКО чужими книгами подтверждено %d (%.1f%%)\n",
		totalEdges, totalOnlyAlien, 100*float64(totalOnlyAlien)/float64(totalEdges))
	fmt.Println()
	fmt.Println("  «Только чужие» — пара, у которой НИ ОДНОГО подтверждения из своей")
	fmt.Println("  области. Именно такие связи и есть шум чужого каталога; пары, где есть")
	fmt.Println("  хотя бы одно своё подтверждение, трогать нельзя — книга по DevOps")
	fmt.Println("  может сказать верное слово про графы.")
}

func countFolders(byDoc map[uint32]string) int {
	seen := map[string]bool{}
	for _, f := range byDoc {
		seen[f] = true
	}
	return len(seen)
}
