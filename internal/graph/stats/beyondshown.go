package stats

// Как часто нужная связь есть в графе, но не попадает в выдачу (этап 104, П3.1).
//
// Для пары «как связаны X и Y» нужная связь известна заранее — X↔Y. Три исхода:
// показана; есть в графе, но отбор её не показал (потеряна на отборе — это и
// есть предмет замера); в графе её нет вовсе (выдаче взять неоткуда).
// Отдельно — почему потеряна: не найдено само понятие или связь срезана
// пределом «связей на понятие». Штатный поиск, штатные числа отбора.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

func beyondShown(cfg *config.Config, g *graph.Graph, c *kb.Collection, sets []string) {
	deps := find.Deps{Coll: c, Graph: g,
		Embedder: kbembed.New(cfg.KB.EmbedOptions(), cfg.EmbedFallback(), 2*time.Minute, nil)}
	fmt.Println("\nП3.1. Нужная связь X↔Y: показана / есть в графе, но потеряна на отборе / в графе нет")
	for _, set := range sets {
		var total, shown, lost, absent, lostEntity, lostCut int
		var examples []string
		for _, p := range readRelationPairs(set) {
			a, ok1 := g.Entities().Lookup(p[0])
			b, ok2 := g.Entities().Lookup(p[1])
			inGraph := ok1 && ok2 && len(g.Edges().Between(a.ID, b.ID)) > 0
			total++
			if !inGraph {
				absent++
				continue
			}
			res, err := find.Search(context.Background(), deps, "Как связаны "+p[0]+" и "+p[1]+"?", find.Opts{})
			if err != nil {
				continue
			}
			if hasPair(res.Relations, p[0], p[1]) {
				shown++
				continue
			}
			lost++
			foundA, foundB := false, false
			for _, e := range res.Entities {
				foundA = foundA || e.ID == a.ID
				foundB = foundB || e.ID == b.ID
			}
			why := "связь срезана отбором соседей"
			if !foundA || !foundB {
				why = "понятие не найдено входом"
				lostEntity++
			} else {
				lostCut++
			}
			if len(examples) < 8 {
				examples = append(examples, fmt.Sprintf("%s ↔ %s — %s", p[0], p[1], why))
			}
		}
		fmt.Printf("\n  %s: пар %d; связь показана %d, потеряна на отборе %d (понятие не найдено %d, срезана %d), в графе нет %d\n",
			set, total, shown, lost, lostEntity, lostCut, absent)
		if len(examples) > 0 {
			fmt.Println("    потеряны:", strings.Join(examples, "; "))
		}
	}
}
