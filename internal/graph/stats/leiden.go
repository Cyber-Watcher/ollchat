package stats

// Лувен против Лейдена на одном графе — этап 90, «третий граф на Leiden
// проекцией» (Н3). Карта не нужна, граф не меняется: считает
// ExperimentPartition с теми же условиями, что у рабочего разбиения.

import (
	"fmt"
	"sort"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

func leidenCompare(g *graph.Graph, resolution, relatedWeight float64, byOrigins bool) {
	fmt.Printf("\nЛувен против Лейдена (этап 90, Н3). Условия рабочего разбиения: γ = %.2f, вес «связано» %.2f, по источникам %v\n",
		resolution, relatedWeight, byOrigins)
	fmt.Println("  Граф не изменяется: считает проекция ExperimentPartition; Лувен здесь — одна фаза без дробления, как в опыте.")
	fmt.Println()
	run := func(name string, leiden bool) graph.PartitionExperiment {
		start := time.Now()
		r := g.ExperimentPartition(graph.PartitionOpts{
			Weights: map[uint8]float64{graph.RelRelated: relatedWeight}, Resolution: resolution,
			ByOrigins: byOrigins, Leiden: leiden, KeepAssignment: true,
		})
		share := 0.0
		if r.Themes > 0 {
			share = 100 * float64(r.Singleton) / float64(r.Themes)
		}
		fmt.Printf("  %-7s тем %7d, одиночных %6d (%.1f%%), медиана %d, крупнейшая %6d, модулярность низ %.4f / верх %.4f, несвязных тем %d, уровней %d %v, за %s\n",
			name, r.Themes, r.Singleton, share, r.Median, r.Largest, r.Modularity, r.ModularityTop, r.Disconnected,
			len(r.LevelThemes), r.LevelThemes, time.Since(start).Round(time.Second))
		return r
	}
	lv := run("Лувен", false)
	ld := run("Лейден", true)

	// Согласие разметок: для каждой темы Лувена — доля её понятий, попавших
	// в одну (самую частую) тему Лейдена, взвешенная по размеру; и наоборот.
	fmt.Printf("  чистота: тема Лувена внутри одной темы Лейдена %.1f%%, тема Лейдена внутри одной темы Лувена %.1f%%\n",
		100*purity(lv.Assign, ld.Assign), 100*purity(ld.Assign, lv.Assign))
	if len(ld.Levels) > 1 {
		fmt.Println("  уровни Лейдена (тем; размеры первых пяти по убыванию):")
		for i, at := range ld.Levels {
			sizes := map[uint32]int{}
			for _, c := range at {
				sizes[c]++
			}
			all := make([]int, 0, len(sizes))
			for _, n := range sizes {
				all = append(all, n)
			}
			sort.Sort(sort.Reverse(sort.IntSlice(all)))
			if len(all) > 5 {
				all = all[:5]
			}
			fmt.Printf("    уровень %d: тем %d, крупнейшие %v\n", i, len(sizes), all)
		}
	}
	// А5 этапа 105: цена перехода в описаниях — сколько прежних описаний
	// перенеслось бы на новые темы (мера carry, порог рабочий).
	if old, err := g.LoadCommunities(); err == nil && old != nil {
		described := 0
		for _, com := range old.List {
			if com.Level == 0 && com.Title != "" {
				described++
			}
		}
		for _, x := range []struct {
			name   string
			assign map[uint32]uint32
		}{{"Лувен", lv.Assign}, {"Лейден", ld.Assign}} {
			r := graph.CarryPreview(old, x.assign, 0)
			sizes := map[uint32]int{}
			for _, c := range x.assign {
				sizes[c]++
			}
			big := 0 // тем от пяти понятий — только они получают описание (MinMembers)
			for _, n := range sizes {
				if n >= 5 {
					big++
				}
			}
			fmt.Printf("  перенос описаний на %s: описанных тем сейчас %d, перенеслось бы %d, потерялось бы %d; тем от 5 понятий %d → описывать заново ≈ %d\n",
				x.name, described, r.Carried, r.Lost, big, max(big-r.Carried, 0))
		}
	}
	fmt.Println()
	fmt.Println("  Как читать: «несвязных тем» у Лейдена обязано быть 0 — это его обещание;")
	fmt.Println("  у Лувена они есть и режутся задним числом (splitDisconnected). Модулярность —")
	fmt.Println("  на одном графе и одном γ, выше лучше. Уровни — иерархия C3→C0 из GraphRAG.")
}

// purity — взвешенная доля понятий темы a, лежащих в самой частой теме b.
func purity(a, b map[uint32]uint32) float64 {
	members := map[uint32][]uint32{}
	for id, c := range a {
		members[c] = append(members[c], id)
	}
	total, agree := 0, 0
	for _, list := range members {
		count := map[uint32]int{}
		best := 0
		for _, id := range list {
			count[b[id]]++
			if count[b[id]] > best {
				best = count[b[id]]
			}
		}
		total += len(list)
		agree += best
	}
	if total == 0 {
		return 0
	}
	return float64(agree) / float64(total)
}
