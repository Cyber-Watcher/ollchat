package stats

// Сколько графа держится на названных книгах (этап 104, П6.6).
//
// **Зачем.** Перед переиндексацией надо знать цену: перечитанная книга
// получает НОВЫЙ номер, прежний помечается удалённым, и граф продолжает
// ссылаться на старые куски — ссылки теряют название книги и страницу
// (сказано прямо в `internal/kb/reindex.go`). Значит книги, попавшие в граф,
// перечитывают вместе с догоном графа, а цена догона — это извлечение заново.
//
// Считается: сколько кусков этих книг разобрано, сколько связей подтверждено
// ТОЛЬКО ими (такие осиротеют) и сколько понятий держится только на них.

import (
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

func bookImpact(g *graph.Graph, c *kb.Collection, needles []string) {
	want := map[uint32]string{}
	for _, b := range c.MatchingDocs(kb.ChunkFilter{}) {
		for _, n := range needles {
			n = strings.TrimSpace(n)
			if n != "" && strings.Contains(strings.ToLower(b.Title+" "+b.Path), strings.ToLower(n)) {
				want[b.ID] = b.Title
				break
			}
		}
	}
	fmt.Printf("\nВклад книг в граф: книг найдено %d\n", len(want))
	if len(want) == 0 {
		return
	}

	// Связи: сколько подтверждены только этими книгами.
	pairs := map[[2]uint32]struct{ ours, all int }{}
	ents := g.Entities().Live()
	var edgesOurs int
	for _, e := range ents {
		for _, ed := range g.Edges().Of(e.ID) {
			// Пара без направления: A→B и B→A — одна связь. С направленным ключом
			// пара, подтверждённая нашей книгой в одну сторону и чужой в другую,
			// считалась «только нашей» (аудит 17.09.2026, M16).
			k := [2]uint32{ed.Src, ed.Dst}
			if k[0] > k[1] {
				k[0], k[1] = k[1], k[0]
			}
			v := pairs[k]
			v.all++
			if _, ok := want[ed.Evidence.Doc]; ok {
				v.ours++
				edgesOurs++
			}
			pairs[k] = v
		}
	}
	var onlyOurs, touched int
	for _, v := range pairs {
		if v.ours == 0 {
			continue
		}
		touched++
		if v.ours == v.all {
			onlyOurs++
		}
	}

	// Понятия: сколько встречаются только в этих книгах.
	var entsOnly int
	for _, e := range ents {
		var ours, all int
		seen := map[uint32]bool{}
		for _, k := range g.Mentions().Of(e.ID) {
			if seen[k.Doc] {
				continue
			}
			seen[k.Doc] = true
			all++
			if _, ok := want[k.Doc]; ok {
				ours++
			}
		}
		if all > 0 && ours == all {
			entsOnly++
		}
	}

	// Сколько кусков этих книг УЖЕ разобрано графом: от этого зависит цена
	// догона (извлечение идёт 0,28–0,30 куска в секунду).
	// Перечитанная книга получает новый номер, и сборка разбирает её ЦЕЛИКОМ,
	// кроме служебных кусков: цена догона — все её рабочие куски, а не только
	// те, что разобраны сейчас.
	var chunksAll, chunksDone, chunksWork int
	for id, title := range want {
		var all, done, work int
		_ = c.EachChunkRef(kb.ChunkFilter{Docs: []uint32{id}}, func(r kb.ChunkRef) error {
			all++
			if !r.TOC && !r.Refs {
				work++
			}
			if _, ok := g.Progress().MarkOf(graph.ChunkKey{Doc: r.Doc, Ord: r.Ord}); ok {
				done++
			}
			return nil
		})
		chunksAll, chunksDone, chunksWork = chunksAll+all, chunksDone+done, chunksWork+work
		fmt.Printf("    %5d кусков (рабочих %5d, разобрано %5d)  %.70s\n", all, work, done, title)
	}
	fmt.Printf("  кусков в этих книгах: %d, из них разобрано графом: %d\n", chunksAll, chunksDone)
	fmt.Printf("  рабочих кусков к разбору после перечитывания: %d\n", chunksWork)
	fmt.Printf("  цена догона графа: %.1f ч карты при 0,28 куска/с (%.1f ч при 0,30)\n",
		float64(chunksWork)/0.28/3600, float64(chunksWork)/0.30/3600)
	fmt.Printf("  записей связей из этих книг: %d\n", edgesOurs)
	fmt.Printf("  пар понятий, которых эти книги касаются: %d\n", touched)
	fmt.Printf("  из них подтверждены ТОЛЬКО этими книгами: %d\n", onlyOurs)
	fmt.Printf("  понятий, встречающихся только в этих книгах: %d\n", entsOnly)
	fmt.Println()
	fmt.Println("  «Только этими» — то, что осиротеет при переиндексации без")
	fmt.Println("  догона графа: связь останется, а выдержку показать будет нечем.")
}
