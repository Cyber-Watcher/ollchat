// seq.go — режим `-only seq`, бывший `seqcheck` (этап 90, слово владельца
// 19.09.2026, перенос — этап 114 Г3): детерминизм извлечения ПО ОДНОМУ
// запросу — куски отправляются модели по одному, дважды подряд, с промптом
// формата 2 и температурой 0. Сравниваются сырые ответы: набор имён понятий
// и набор троек (src, dst, тип) — через общее ядро setcmp.go. Ничего не пишет
// ни в граф, ни в коллекцию; карта нужна.
package probes

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/graphex"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// parseChunkKeys — список кусков через запятую («13#7,4#109») в ключи графа.
// Пустые элементы (лишние запятые, пробелы по краям) пропускаются.
func parseChunkKeys(list string) ([]graph.ChunkKey, error) {
	var keys []graph.ChunkKey
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		k, err := graph.ParseChunkKey(s)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// runSeq — режим seq.
func runSeq(stdout io.Writer, cfg *config.Config, collName, chunks string, n int, timeout time.Duration) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	keys, err := parseChunkKeys(chunks)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		if err := c.EachChunkRef(kb.ChunkFilter{Docs: []uint32{4}}, func(r kb.ChunkRef) error {
			if len(keys) < n && r.Ord%5 == 0 && !r.TOC && !r.Refs {
				keys = append(keys, graph.ChunkKey{Doc: r.Doc, Ord: r.Ord})
			}
			return nil
		}); err != nil {
			return err
		}
	}
	o := cfg.Graph.ExtractOptions()
	o.Temperature = 0
	o.Workers = 1
	ex := graphex.New(o, cfg.Servers[0].URL, timeout, nil)
	if ex == nil {
		return fmt.Errorf("извлечение не настроено")
	}
	fmt.Fprintf(stdout, "модель %s, температура 0, по одному запросу; кусков %d\n", ex.Model(), len(keys))
	ctx := context.Background()
	same, diffE, diffR := 0, 0, 0
	for _, key := range keys {
		ci, ok := c.ChunkByRef(key.Doc, key.Ord)
		if !ok {
			fmt.Fprintf(stdout, "%s: нет куска\n", key)
			continue
		}
		title := ci.Book.Title
		if title == "" {
			title = filepath.Base(ci.Book.Path)
		}
		user := graph.UserPrompt(title, ci.Unit, ci.UnitFrom, ci.UnitTo, ci.Text)
		var ents [2][]NameSet
		var rels [2][]EdgeSet
		okBoth := true
		for i := 0; i < 2; i++ {
			answer, err := ex.Extract(ctx, graph.SystemPromptV2, user)
			if err != nil {
				fmt.Fprintf(stdout, "%s: сбой запроса %d: %v\n", key, i+1, err)
				okBoth = false
				break
			}
			facts, err := graph.ParseFactsFor(graph.FormatV2, answer, ci.Text)
			if err != nil {
				fmt.Fprintf(stdout, "%s: разбор ответа %d: %v\n", key, i+1, err)
				okBoth = false
				break
			}
			for _, e := range facts.Entities {
				ents[i] = append(ents[i], NameSet{graph.Normalize(e.Name): true})
			}
			for _, r := range facts.Relations {
				rels[i] = append(rels[i], EdgeSet{
					Src:  NameSet{graph.Normalize(r.Src): true},
					Dst:  NameSet{graph.Normalize(r.Dst): true},
					Type: r.Type,
				})
			}
		}
		if !okBoth {
			continue
		}
		_, eOnly1, eOnly2 := MatchNames(ents[0], ents[1])
		_, rOnly1, rOnly2 := MatchEdges(rels[0], rels[1], false)
		eEqual := len(eOnly1) == 0 && len(eOnly2) == 0
		rEqual := len(rOnly1) == 0 && len(rOnly2) == 0
		switch {
		case eEqual && rEqual:
			same++
			fmt.Fprintf(stdout, "%s: совпало (понятий %d, связей %d)\n", key, len(ents[0]), len(rels[0]))
		default:
			if !eEqual {
				diffE++
			}
			if !rEqual {
				diffR++
			}
			fmt.Fprintf(stdout, "%s: РАЗОШЛОСЬ — понятия %d/%d (%s), связи %d/%d (%s)\n", key,
				len(ents[0]), len(ents[1]), verdict(eEqual), len(rels[0]), len(rels[1]), verdict(rEqual))
			for _, i := range eOnly1 {
				fmt.Fprintf(stdout, "    только в 1-м: %s\n", ents[0][i].first())
			}
			for _, j := range eOnly2 {
				fmt.Fprintf(stdout, "    только во 2-м: %s\n", ents[1][j].first())
			}
			for _, i := range rOnly1 {
				fmt.Fprintf(stdout, "    только в 1-м: %s|%s|%s\n", rels[0][i].Src.first(), rels[0][i].Type, rels[0][i].Dst.first())
			}
			for _, j := range rOnly2 {
				fmt.Fprintf(stdout, "    только во 2-м: %s|%s|%s\n", rels[1][j].Src.first(), rels[1][j].Type, rels[1][j].Dst.first())
			}
		}
	}
	fmt.Fprintf(stdout, "\nитого: кусков %d, совпали оба набора у %d, понятия разошлись у %d, связи у %d\n", len(keys), same, diffE, diffR)
	return nil
}

func verdict(ok bool) string {
	if ok {
		return "равны"
	}
	return "разные"
}
