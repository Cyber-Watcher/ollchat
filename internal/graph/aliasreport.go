package graph

import (
	"sort"
	"strings"
)

// Отчёт по журналу синонимов графа формата 2 — для доктора.
//
// **Зачем.** Опытный граф пишет каждое вхождение синонима с куском-источником
// (aliases.log), и это единственное место, где видно, что модель извлечения
// делает с синонимами на самом деле: сколько их, какого рода, сколько из них —
// собственные имена других узлов. Без отчёта первые ночи сборки опытного графа
// смотреть было бы нечем (этап 90, пункт 6).
//
// Каждая запись журнала по устройству схемы 2 буквально присутствует в своём
// куске (extract.go, clean): отчёт это не перепроверяет, а считает.

// AliasReport — что в журнале синонимов.
type AliasReport struct {
	Records  int // вхождений синонимов (запись журнала)
	Chunks   int // в скольких кусках
	Pairs    int // разных пар «понятие — написание»
	Entities int // у скольких понятий есть хоть один синоним

	// По виду пары «имя понятия — синоним», считается по разным парам;
	// четыре счётчика в сумме дают Pairs:
	Translations int // другой алфавит: перевод
	Acronyms     int // аббревиатура одного от другого
	Other        int // иное написание того же
	// Clashes — синоним совпадает с собственным именем другого понятия:
	// кандидат в двойники либо род/вид, а не синоним.
	Clashes int

	// Top — самые частые пары по вхождениям, для взгляда глазами.
	Top []AliasPairCount
}

// AliasPairCount — одна пара «понятие — синоним» и сколько раз встречена.
type AliasPairCount struct {
	Entity string
	Alias  string
	Count  int
	Clash  bool
}

// AliasReportOf строит отчёт по открытому графу. У графа формата 1 журнала
// нет — возвращается ok=false.
func (g *Graph) AliasReportOf(top int) (AliasReport, bool) {
	var r AliasReport
	al := g.Aliases()
	if al == nil {
		return r, false
	}
	type pairKey struct {
		entity uint32
		norm   string
	}
	recs := al.All()
	r.Records = len(recs)
	chunks := map[ChunkKey]bool{}
	pairs := map[pairKey]int{}
	for _, rec := range recs {
		chunks[rec.Chunk] = true
		pairs[pairKey{rec.Entity, rec.Norm}]++
	}
	r.Chunks = len(chunks)
	r.Pairs = len(pairs)

	ents := map[uint32]bool{}
	var list []AliasPairCount
	for k, n := range pairs {
		ents[k.entity] = true
		ent, ok := g.ents.Get(k.entity)
		name := "?"
		if ok {
			name = ent.Name
		}
		clash := false
		if id, found := g.ents.byKey[k.norm]; found && id != k.entity {
			if other := g.ents.rawAt(id); other.Norm == k.norm {
				clash = true
			}
		}
		// Чужое имя — свой вид: это не перевод и не иное написание, а кандидат
		// в двойники (либо род и вид), и в остальные счётчики оно не входит.
		switch {
		case clash:
			r.Clashes++
		case ok && script(ent.Norm) != script(k.norm) && script(k.norm) != 0:
			r.Translations++
		case ok && (acronymOf(k.norm, strings.Fields(ent.Norm)) || acronymOf(ent.Norm, strings.Fields(k.norm))):
			r.Acronyms++
		default:
			r.Other++
		}
		list = append(list, AliasPairCount{Entity: name, Alias: k.norm, Count: n, Clash: clash})
	}
	r.Entities = len(ents)
	sort.Slice(list, func(i, j int) bool {
		if list[i].Count != list[j].Count {
			return list[i].Count > list[j].Count
		}
		return list[i].Alias < list[j].Alias
	})
	if top > 0 && len(list) > top {
		list = list[:top]
	}
	r.Top = list
	return r, true
}
