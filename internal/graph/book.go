package graph

// Подграф одной книги: что она добавила в граф.
//
// **Зачем.** У каждого упоминания и каждой связи уже записан номер книги (Doc),
// поэтому «что дала эта книга» и «выброси её вклад» — это ВЫБОРКА по журналам,
// а не новое хранение. Закрывает переизвлечение испорченной книги без пересборки
// всего графа: пометить её вклад отброшенным, переизвлечь только её, доложить.
//
// Ничего не переписывает: только чтение. Отбрасывание вклада (отдельная работа)
// пойдёт журналом, как склейки двойников; реестр понятий при этом цел.

import "sort"

// BookContribution — вклад одной книги в граф.
type BookContribution struct {
	Book     uint32
	Mentions int      // упоминаний понятий, взятых из этой книги
	Edges    int      // связей, подтверждённых её кусками
	Entities []uint32 // понятия, которые эта книга упоминает
	OnlyHere []uint32 // из них — те, что НЕ упомянуты ни одной другой книгой
}

// Contribution собирает вклад книги doc за один проход по упоминаниям.
//
// OnlyHere — понятия, которые держатся на одной этой книге: выбросишь её вклад,
// и они исчезнут из графа вовсе. По ним видна цена отбрасывания.
func (g *Graph) Contribution(doc uint32) BookContribution {
	c := BookContribution{Book: doc}

	inThis := map[uint32]bool{}  // понятие упомянуто в этой книге
	inOther := map[uint32]bool{} // и хотя бы в одной другой
	g.ment.eachMention(func(ent uint32, key uint64) {
		if UnpackChunk(key).Doc == doc {
			if !inThis[ent] {
				inThis[ent] = true
			}
			c.Mentions++
		} else {
			inOther[ent] = true
		}
	})

	for ent := range inThis {
		c.Entities = append(c.Entities, ent)
		if !inOther[ent] {
			c.OnlyHere = append(c.OnlyHere, ent)
		}
	}
	sort.Slice(c.Entities, func(i, j int) bool { return c.Entities[i] < c.Entities[j] })
	sort.Slice(c.OnlyHere, func(i, j int) bool { return c.OnlyHere[i] < c.OnlyHere[j] })

	g.edge.eachEdge(func(e Edge) {
		if e.Evidence.Doc == doc {
			c.Edges++
		}
	})
	return c
}
