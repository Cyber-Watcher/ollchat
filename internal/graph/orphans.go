package graph

import "sort"

// Понятия без единой связи: сколько их и откуда они берутся.
//
// **Откуда взялось.** Строение графа (этап 101, Г5) впервые показало число:
// 10 997 живых понятий из 228 187 (4%) не имеют ни одной связи. Такое понятие
// не попадает в тему никогда — разбиение считается по связям, — а значит,
// не видно ни обзору тем, ни входу в граф через тему.
//
// Причин может быть три, и они лечатся по-разному:
//
//  1. Модель извлекла сущность и не назвала ни одного отношения. Это брак
//     извлечения: чинится промптом и только на новой сборке.
//  2. Связи у понятия были, но после наложения склеек оба конца сошлись
//     в одно понятие, и связь отброшена как петля. Это цена склейки двойников,
//     и она законна: «сборщик мусора» и `Garbage collection` — одно и то же,
//     связь между ними бессмысленна.
//  3. Связи вели к понятиям удалённых книг и ушли вместе с ними.
//
// Различить их можно только по сырым записям: у случая 1 их нет вовсе,
// у случая 2 они есть, но после Resolve превращаются в петли.

// Orphans — разбор понятий без связей.
type Orphans struct {
	Total int // живых понятий без единой связи

	// NoRawEdges — из них тех, у кого нет ни одной записи связи вообще:
	// модель извлекла сущность, не назвав отношений (случай 1).
	NoRawEdges int
	// LostToMerge — тех, у кого записи связей есть, но после наложения склеек
	// все они выродились в петли (случай 2).
	LostToMerge int
	// Survivors — сколько одиночек сами поглотили кого-то при склейке.
	Survivors int

	// Empty — пустые узлы: ни связей, ни упоминаний. Показать по ним нечего,
	// и смысловой вход их не предлагает (`emptyNode`). Это следы чистки
	// от оглавлений (этап 99): записи убраны, номера понятий оставлены.
	Empty int

	// Mentioned — сколько одиночек вообще встречались в кусках.
	Mentioned int
	// SingleMention — из них те, что встретились ровно один раз.
	SingleMention int

	// ByType — сколько одиночек какого типа.
	ByType map[string]int

	// Sample — примеры: самые упоминаемые одиночки.
	Sample []OrphanExample
}

// OrphanExample — одно понятие без связей, для показа глазами.
type OrphanExample struct {
	ID       uint32
	Name     string
	Type     string
	Count    int // упоминаний
	RawEdges int // записей связей до наложения склеек
}

// Orphans разбирает понятия без связей: сколько их и почему они такие.
//
// Обход по всему графу в памяти; карта и модель не нужны.
func (g *Graph) Orphans(sample int) Orphans {
	if sample <= 0 {
		sample = 20
	}
	out := Orphans{ByType: map[string]int{}}

	for _, ent := range g.Entities().Live() {
		if len(g.Edges().Of(ent.ID)) > 0 || len(g.Edges().incoming(ent.ID)) > 0 {
			continue
		}
		out.Total++
		out.ByType[ent.Type]++

		raw := len(g.Edges().ofRaw(ent.ID)) + len(g.Edges().toRaw(ent.ID))
		if raw == 0 {
			out.NoRawEdges++
		} else {
			out.LostToMerge++
		}
		if len(g.Merges().Absorbed(ent.ID)) > 0 {
			out.Survivors++
		}

		switch n := len(g.Mentions().Of(ent.ID)); {
		case n == 0:
			out.Empty++
		case n == 1:
			out.Mentioned++
			out.SingleMention++
		default:
			out.Mentioned++
		}

		out.Sample = append(out.Sample, OrphanExample{
			ID: ent.ID, Name: ent.Name, Type: ent.Type, Count: ent.Count, RawEdges: raw,
		})
	}

	// В примеры идут самые упоминаемые: если понятие встречено в книгах сорок
	// раз и не связано ни с чем, это интереснее, чем случайное имя из одного куска.
	sort.Slice(out.Sample, func(i, j int) bool {
		if out.Sample[i].Count != out.Sample[j].Count {
			return out.Sample[i].Count > out.Sample[j].Count
		}
		return out.Sample[i].ID < out.Sample[j].ID
	})
	if len(out.Sample) > sample {
		out.Sample = out.Sample[:sample]
	}
	return out
}
