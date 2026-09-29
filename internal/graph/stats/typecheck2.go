package stats

// Нужны ли в графе люди и организации (этап 104, П2.3).
//
// **Зачем.** Разбор одиночек 16.09.2026: из 12 571 понятия без связей 13,4%
// имеют тип `человек` и 4,2% — `организация`. Это имена авторов с обложек
// и названия издательств: `Rheinwerk Publishing`, `Casey West`,
// `Benjamin Franklin`. В графе технических книг от них пользы не видно.
//
// **Но прежде чем перестать их извлекать, надо посмотреть на тех, у кого
// связи ЕСТЬ.** Если связи осмысленные («Rob Pike —создал→ Go»), тип нужен;
// если это соседство по обложке — не нужен.
//
// Ничего не меняет.

import (
	"fmt"
	"math/rand"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

func typeUsefulness(g *graph.Graph, types []string, sample int, show int, seed int64) {
	want := map[string]bool{}
	for _, t := range types {
		want[t] = true
	}
	live := g.Entities().Live()
	rnd := rand.New(rand.NewSource(seed))

	// Понятий таких типов десятки тысяч — считаем ВСЕ, а не выборку: доля
	// получается точной, и спорить о способе выбора не о чем (17.09.2026).
	// Случайность остаётся только в том, какие примеры показать.
	_ = sample
	var total, withEdges, checked int
	var edged []graph.Entity
	degSum := 0
	for _, e := range live {
		if !want[e.Type] {
			continue
		}
		total++
		checked++
		nb := g.Edges().Neighbors(e.ID)
		if len(nb) == 0 {
			continue
		}
		withEdges++
		degSum += len(nb)
		edged = append(edged, e)
	}
	rnd.Shuffle(len(edged), func(i, j int) { edged[i], edged[j] = edged[j], edged[i] })
	examples := make([]string, 0, show)
	for _, e := range edged {
		if len(examples) >= show {
			break
		}
		names := ""
		for i, n := range g.Edges().Neighbors(e.ID) {
			if i >= 3 {
				break
			}
			if ent, ok := g.Entities().Get(n.ID); ok {
				if names != "" {
					names += ", "
				}
				names += fmt.Sprintf("%s (%d)", cut(ent.Name, 24), n.Count)
			}
		}
		examples = append(examples, fmt.Sprintf("%-28s [%s] → %s",
			cut(e.Name, 28), e.Type, names))
	}

	fmt.Printf("\nП2.3. Нужны ли в графе типы %v\n\n", types)
	fmt.Printf("  понятий таких типов всего: %d\n", total)
	fmt.Printf("  проверено ВСЕ: %d, из них СО СВЯЗЯМИ: %d (%.1f%%)\n",
		checked, withEdges, 100*float64(withEdges)/float64(max(checked, 1)))
	if withEdges > 0 {
		fmt.Printf("  связей у таких понятий в среднем: %.1f\n", float64(degSum)/float64(withEdges))
	}
	if len(examples) > 0 {
		fmt.Println("\n  примеры со связями (понятие → три сильнейших соседа):")
		for _, x := range examples {
			fmt.Println("   ", x)
		}
	}
	fmt.Println()
	fmt.Println("  Решать по примерам: «Rob Pike → Go» — связь по делу, тип нужен;")
	fmt.Println("  «издательство → название серии» — соседство по обложке, не нужен.")
}
