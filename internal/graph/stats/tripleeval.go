package stats

import (
	"context"
	"fmt"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

// tripleEval — находится ли САМА связь X↔Y по вектору вопроса среди ближайших
// троек индекса (этап 105, В1: «новая мера, не только концы»). Вход по понятиям
// эту меру не даёт: на парах концы находятся словесно и так, а на тематических
// вопросах тройкам не достаётся места во входе.
//
// Для каждой пары набора: есть ли в графе прямая связь (только такие находимы),
// стоит ли она первой среди ближайших троек, входит ли в пятёрку; и медиана
// близости найденной связи против медианы первой тройки у пар, где связи нет.
func tripleEval(cfg *config.Config, c *kb.Collection, g *graph.Graph, path string, k int) {
	var set struct {
		Case []struct {
			Query    string `toml:"query"`
			ConceptA string `toml:"concept_a"`
			ConceptB string `toml:"concept_b"`
		} `toml:"case"`
	}
	if _, err := toml.DecodeFile(path, &set); err != nil {
		die(err)
	}
	if !g.EdgeVectorsInfo().Ready {
		die(fmt.Errorf("индекса троек нет: ollchat --graph-embed-edges <коллекция>"))
	}
	deps := find.Deps{Coll: c, Graph: g,
		Embedder: kbembed.New(cfg.KB.EmbedOptions(), cfg.EmbedFallback(), 2*time.Minute, nil)}
	m := g.Merges()

	var unknown, direct, top1, topK, noVec int
	var ranks []int
	fmt.Printf("\nСвязь по вектору вопроса среди ближайших %d троек (%s, пар %d)\n", k, path, len(set.Case))
	for _, cs := range set.Case {
		a, okA := g.Entities().Lookup(cs.ConceptA)
		b, okB := g.Entities().Lookup(cs.ConceptB)
		if !okA || !okB {
			unknown++
			continue
		}
		ai, bi := m.Resolve(a.ID), m.Resolve(b.ID)
		has := len(g.Edges().Between(ai, bi)) > 0 || len(g.Edges().Between(bi, ai)) > 0
		if !has {
			continue
		}
		direct++
		qv, _, note := find.QueryVector(context.Background(), deps, cs.Query, find.Opts{Semantic: true, QueryTimeout: 2 * time.Minute})
		if len(qv) == 0 {
			noVec++
			if noVec == 1 {
				fmt.Printf("  вектор вопроса не посчитан: %s\n", note)
			}
			continue
		}
		hits := g.NearestTriples(qv, k)
		rank := 0
		for i, h := range hits {
			s, d := m.Resolve(h.Key.Src), m.Resolve(h.Key.Dst)
			if (s == ai && d == bi) || (s == bi && d == ai) {
				rank = i + 1
				break
			}
		}
		switch {
		case rank == 1:
			top1++
			topK++
		case rank > 1:
			topK++
		}
		if rank > 0 {
			ranks = append(ranks, rank)
		}
		mark := "—"
		if rank > 0 {
			mark = fmt.Sprintf("%d", rank)
		}
		first := ""
		if len(hits) > 0 {
			sa, _ := g.Entities().Get(m.Resolve(hits[0].Key.Src))
			da, _ := g.Entities().Get(m.Resolve(hits[0].Key.Dst))
			first = fmt.Sprintf("%s —%s→ %s (%.3f)", cut(sa.Name, 22), graph.RelName(hits[0].Key.Type), cut(da.Name, 22), hits[0].Score)
		}
		fmt.Printf("  %-3s %-48s первая: %s\n", mark, cut(cs.Query, 48), first)
	}
	fmt.Printf("\n  понятие не в графе %d; пар с прямой связью в графе (находимых) %d, без вектора %d\n", unknown, direct, noVec)
	// Знаменатель — пары, которые ПРОВЕРЯЛИСЬ: без вектора вопроса связь
	// не искалась вовсе, и считать её «не найденной» значит занижать долю.
	if checked := direct - noVec; checked > 0 {
		fmt.Printf("  проверено пар %d; связь первой тройкой: %d (%.1f%%); в первых %d: %d (%.1f%%)\n",
			checked, top1, pct(top1, checked), k, topK, pct(topK, checked))
	}
}
