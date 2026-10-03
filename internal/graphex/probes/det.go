// det.go — режим `-only det`, бывший `detcheck` (этап 90, слово владельца
// 19.09.2026, перенос — этап 114 Г3): детерминизм сборки построчно.
// Проверочный граф (те же куски, разобранные второй раз при температуре 0)
// сравнивается с опорным графом по каждому куску: набор понятий (по
// нормализованному имени, с учётом синонимов записи) и набор связей — через
// общее ядро setcmp.go. Только читает оба графа; карта не нужна.
package probes

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// runDet — режим det.
func runDet(stdout io.Writer, cfg *config.Config, collName, gname, cname string, show int) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	open := func(name string) (*graph.Graph, error) {
		if err := cfg.UseGraph(name); err != nil {
			return nil, err
		}
		return graph.Open(c.Dir(), c.ChunkCount(), cfg.Graph.Rules())
	}
	ref, err := open(gname)
	if err != nil {
		return err
	}
	defer ref.Close()
	chk, err := open(cname)
	if err != nil {
		return err
	}
	defer chk.Close()

	// Имена понятия: нормализованное имя и синонимы — как NameSet ядра.
	names := func(g *graph.Graph, id uint32) NameSet {
		out := NameSet{}
		if e, ok := g.Entities().Get(id); ok {
			out[graph.Normalize(e.Name)] = true
			for _, a := range e.Aliases {
				out[graph.Normalize(a)] = true
			}
		}
		return out
	}
	// Связи куска: (имена src, имена dst, тип) — как EdgeSet ядра.
	edgesOf := func(g *graph.Graph, key graph.ChunkKey, ids []uint32) []EdgeSet {
		var out []EdgeSet
		seen := map[string]bool{}
		for _, id := range ids {
			for _, ed := range g.Edges().Of(id) {
				if ed.Evidence != key {
					continue
				}
				k := fmt.Sprintf("%d-%d-%d", ed.Src, ed.Dst, ed.Type)
				if seen[k] {
					continue
				}
				seen[k] = true
				out = append(out, EdgeSet{Src: names(g, ed.Src), Dst: names(g, ed.Dst), Type: graph.RelName(ed.Type)})
			}
		}
		return out
	}

	chunks, entSame, entOnlyChk, entOnlyRef := 0, 0, 0, 0
	edgSame, edgOnlyChk, edgOnlyRef := 0, 0, 0
	skipped := 0
	var diffs []string
	// Разобранные куски проверочного графа — по его упоминаниям и отметкам.
	keys := map[uint64]graph.ChunkKey{}
	for _, e := range chk.Entities().Live() {
		for _, k := range chk.Mentions().Of(e.ID) {
			keys[k.Pack()] = k
		}
	}
	// Порядок обхода — по ссылке на кусок: расхождений печатается только
	// первые show, и при обходе карты их состав менялся между прогонами.
	packed := make([]uint64, 0, len(keys))
	for k := range keys {
		packed = append(packed, k)
	}
	slices.Sort(packed)
	for _, pk := range packed {
		key := keys[pk]
		if !ref.Progress().Done(key) {
			skipped++
			continue
		}
		chunks++
		a := chk.Mentions().In(key)
		b := ref.Mentions().In(key)
		an := make([]NameSet, len(a))
		for i, id := range a {
			an[i] = names(chk, id)
		}
		bn := make([]NameSet, len(b))
		for i, id := range b {
			bn[i] = names(ref, id)
		}
		matched, onlyChk, onlyRef := MatchNames(an, bn)
		entSame += matched
		entOnlyChk += len(onlyChk)
		entOnlyRef += len(onlyRef)
		for _, i := range onlyChk {
			if len(diffs) < show {
				diffs = append(diffs, fmt.Sprintf("%s: понятие только во втором прогоне: %q", key, an[i].first()))
			}
		}
		for _, j := range onlyRef {
			if len(diffs) < show {
				diffs = append(diffs, fmt.Sprintf("%s: понятие только в lab: %q", key, bn[j].first()))
			}
		}

		ea, eb := edgesOf(chk, key, a), edgesOf(ref, key, b)
		eMatched, eOnlyChk, eOnlyRef := MatchEdges(ea, eb, true)
		edgSame += eMatched
		edgOnlyChk += len(eOnlyChk)
		edgOnlyRef += len(eOnlyRef)
		for _, i := range eOnlyChk {
			if len(diffs) < show {
				diffs = append(diffs, fmt.Sprintf("%s: связь только во втором прогоне: %s —%s→ %s", key, ea[i].Src.first(), ea[i].Type, ea[i].Dst.first()))
			}
		}
		for _, j := range eOnlyRef {
			if len(diffs) < show {
				diffs = append(diffs, fmt.Sprintf("%s: связь только в lab: %s —%s→ %s", key, eb[j].Src.first(), eb[j].Type, eb[j].Dst.first()))
			}
		}
	}
	fmt.Fprintf(stdout, "детерминизм сборки: кусков сравнено %d (пропущено — в lab не разобраны: %d)\n", chunks, skipped)
	pct := func(a, b int) float64 {
		if a+b == 0 {
			return 100
		}
		return 100 * float64(a) / float64(a+b)
	}
	fmt.Fprintf(stdout, "  понятия: совпало %d, только во втором прогоне %d, только в lab %d — совпадение %.1f%%\n",
		entSame, entOnlyChk, entOnlyRef, pct(entSame, entOnlyChk+entOnlyRef))
	fmt.Fprintf(stdout, "  связи:   совпало %d, только во втором прогоне %d, только в lab %d — совпадение %.1f%%\n",
		edgSame, edgOnlyChk, edgOnlyRef, pct(edgSame, edgOnlyChk+edgOnlyRef))
	fmt.Fprintln(stdout, "  Совпадение — по нормализованному имени с учётом синонимов записи; остаток расхождений,")
	fmt.Fprintln(stdout, "  если он есть, читать глазами: ключи реестра у графа из 200 кусков и из 19 тысяч могут разойтись.")
	for _, d := range diffs {
		fmt.Fprintln(stdout, "   ", strings.TrimSpace(d))
	}
	return nil
}
