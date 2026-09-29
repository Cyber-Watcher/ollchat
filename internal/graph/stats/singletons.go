package stats

// Одиночные темы: откуда берутся и можно ли их приклеить — этап 103, Ш1.3а.
//
// Замер Ш1.2 показал, что γ (resolution) одиночных тем не порождает вовсе:
// ни при 1, ни при 10 их ноль. В рабочем же разбиении их 14 311 из 55 935.
// Разница между проекцией и рабочим путём одна — **дробление крупных тем**
// (`max_community` = 200, `split_depth` = 6): тема на 6 666 понятий режется
// на куски по двести, и осколки по одному понятию — цена этого разреза.
//
// Здесь считается, что с осколками делать. Вопрос не «сколько их» (это знает
// доктор), а **есть ли куда их приклеить**: если у понятия-одиночки есть связи
// с понятиями других тем, оно может уйти в ту тему, с которой связано сильнее
// всего, и обзор ничего не потеряет. Если связей нет — приклеивать некуда,
// и это другая беда (понятие без связей, их 12 602).
//
// Карта не нужна, граф только на чтение.

import (
	"fmt"
	"sort"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// singletonStats разбирает одиночные темы рабочего разбиения.
func singletonStats(g *graph.Graph) {
	comms, err := g.LoadCommunities()
	die(err)
	if comms == nil {
		fmt.Println("сообщества не размечены: сперва ollchat --graph-communities")
		return
	}

	small := comms.Level(0)
	// Тема каждого понятия — чтобы смотреть, куда ведут связи одиночки.
	themeOf := map[uint32]int{}
	sizeOf := map[int]int{}
	for _, c := range small {
		sizeOf[c.ID] = len(c.Members)
		for _, m := range c.Members {
			themeOf[m] = c.ID
		}
	}

	var singles []graph.Community
	for _, c := range small {
		if len(c.Members) == 1 {
			singles = append(singles, c)
		}
	}

	// Разбор каждой одиночки: есть ли связи наружу и куда они ведут.
	var (
		noEdges     int             // связей нет вовсе — приклеивать нечем
		onlySingles int             // связи только к другим одиночкам
		gluable     int             // есть куда уйти: связь с непустой темой
		targets     = map[int]int{} // тема-получатель → сколько одиночек уйдёт
		degrees     []int
	)
	for _, c := range singles {
		id := c.Members[0]
		byTheme := map[int]float64{}
		deg := 0
		for _, ed := range g.Edges().Of(id) {
			other := ed.Dst
			if other == id {
				other = ed.Src
			}
			if other == id {
				continue
			}
			deg++
			t, ok := themeOf[other]
			if !ok || t == c.ID {
				continue
			}
			w := float64(ed.Weight)
			if w <= 0 {
				w = 1
			}
			byTheme[t] += w
		}
		degrees = append(degrees, deg)
		if deg == 0 {
			noEdges++
			continue
		}
		// Куда бы ушло понятие: тема с наибольшим суммарным весом связей.
		best, bestW := -1, 0.0
		for t, w := range byTheme {
			if w > bestW || (w == bestW && t < best) {
				best, bestW = t, w
			}
		}
		if best < 0 {
			onlySingles++
			continue
		}
		if sizeOf[best] == 1 {
			onlySingles++
			continue
		}
		gluable++
		targets[best]++
	}

	sort.Ints(degrees)
	median := 0
	if len(degrees) > 0 {
		median = degrees[len(degrees)/2]
	}

	fmt.Printf("\nШ1.3а. Одиночные темы рабочего разбиения (этап 103)\n")
	fmt.Printf("  тем нижнего уровня: %d, из них одиночных: %d (%.1f%%)\n",
		len(small), len(singles), 100*float64(len(singles))/float64(len(small)))
	if len(singles) == 0 {
		return
	}
	fmt.Printf("  связей у понятия-одиночки: медиана %d\n", median)
	fmt.Println()
	fmt.Printf("  приклеить есть куда (связь с непустой темой):   %6d (%.1f%%)\n",
		gluable, 100*float64(gluable)/float64(len(singles)))
	fmt.Printf("  связи только к таким же одиночкам:              %6d (%.1f%%)\n",
		onlySingles, 100*float64(onlySingles)/float64(len(singles)))
	fmt.Printf("  связей нет вовсе — приклеивать нечем:           %6d (%.1f%%)\n",
		noEdges, 100*float64(noEdges)/float64(len(singles)))
	fmt.Println()
	fmt.Printf("  тем-получателей: %d (в среднем по %.1f одиночки на тему)\n",
		len(targets), float64(gluable)/float64(max(len(targets), 1)))
	fmt.Printf("  тем стало бы: %d вместо %d\n", len(small)-gluable, len(small))
	fmt.Println()
	fmt.Println("  Что это значит: «приклеить есть куда» — понятие связано с темой,")
	fmt.Println("  где есть другие понятия, и уход туда обзор не портит. «Связей нет")
	fmt.Println("  вовсе» — другая беда (понятия без связей), разрезом она не лечится.")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
