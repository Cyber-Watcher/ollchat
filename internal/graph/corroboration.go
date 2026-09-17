package graph

import "sort"

// Подтверждения связей по ИСТОЧНИКАМ, а не по кускам (этап 101, Г9).
//
// **Откуда взялось.** «RAG 2.0» (Nayak, 2026, разд. 67): «Count evidence
// origins, not page count» — считайте источники, а не страницы: повторение
// делает уверенность громче, но не обоснованнее. У нас вес связи и число
// подтверждений считаются кусками, а соседние куски перекрываются по
// построению (kb.DefaultChunkOpts: 1200 знаков шагом 900, то есть треть
// текста лежит в двух кусках сразу). Фраза из зоны перекрытия извлекается
// дважды и даёт связи два подтверждения за одно упоминание.
//
// **Замер 10.09.2026 на графе books** (735 012 пар понятий): на одном куске
// 73.1%, ещё 66 863 пары (33.9% многокусочных) держатся только на соседних
// кусках одной книги, и проверка по тексту 4 000 таких пар показала, что
// 96% проверяемых — копия из перекрытия. Итого связей с одним источником
// 82.3%, а подтверждений в сумме на 16.2% меньше, чем кусков.
//
// Считается обходом в памяти по открытому графу: карта и модель не нужны,
// файлы графа не трогаются.

// Corroboration — подтверждения связей по источникам.
type Corroboration struct {
	Pairs  int // пар понятий со связью
	Single int // подтверждённых одним куском
	// AdjacentOnly — подтверждённых двумя и более кусками, но все они —
	// соседние куски одной книги: почти наверняка одна фраза из перекрытия.
	AdjacentOnly int
	// ConfChunks и ConfOrigins — подтверждений всего: кусками и источниками,
	// где соседние куски одной книги считаются одним источником.
	ConfChunks  int
	ConfOrigins int
}

// SingleOriginShare — доля связей с одним источником, в процентах.
func (c Corroboration) SingleOriginShare() int {
	if c.Pairs == 0 {
		return 0
	}
	return 100 * (c.Single + c.AdjacentOnly) / c.Pairs
}

// SingleChunkShare — доля связей на одном куске, в процентах (прежняя мера).
func (c Corroboration) SingleChunkShare() int {
	if c.Pairs == 0 {
		return 0
	}
	return 100 * c.Single / c.Pairs
}

// InflationShare — какая доля подтверждений приходится на копии из
// перекрытия, в процентах.
func (c Corroboration) InflationShare() int {
	if c.ConfChunks == 0 {
		return 0
	}
	return 100 * (c.ConfChunks - c.ConfOrigins) / c.ConfChunks
}

// Corroboration считает подтверждения связей по источникам.
func (g *Graph) Corroboration() Corroboration {
	type pairKey struct{ a, b uint32 }
	conf := map[pairKey]map[ChunkKey]bool{}
	for _, ent := range g.Entities().Live() {
		for _, ed := range g.Edges().Of(ent.ID) {
			if ed.Evidence.Doc == 0 {
				continue
			}
			k := pairKey{ed.Src, ed.Dst}
			if k.a > k.b {
				k.a, k.b = k.b, k.a
			}
			if conf[k] == nil {
				conf[k] = map[ChunkKey]bool{}
			}
			conf[k][ed.Evidence] = true
		}
	}
	var out Corroboration
	for _, chunks := range conf {
		out.Pairs++
		out.ConfChunks += len(chunks)
		if len(chunks) == 1 {
			out.Single++
			out.ConfOrigins++
			continue
		}
		byDoc := map[uint32][]uint32{}
		for ck := range chunks {
			byDoc[ck.Doc] = append(byDoc[ck.Doc], ck.Ord)
		}
		origins := 0
		for _, ords := range byDoc {
			origins += originsOf(ords)
		}
		out.ConfOrigins += origins
		if len(byDoc) == 1 && origins == 1 {
			out.AdjacentOnly++
		}
	}
	return out
}

// originsOf — сколько источников среди кусков одной книги: цепочка кусков
// с номерами подряд считается одним.
func originsOf(ords []uint32) int {
	sort.Slice(ords, func(i, j int) bool { return ords[i] < ords[j] })
	n := 1
	for i := 1; i < len(ords); i++ {
		if ords[i] != ords[i-1]+1 {
			n++
		}
	}
	return n
}

// originRec — одно подтверждение пары внутри книги: номер куска и вес записи.
type originRec struct {
	ord uint32
	w   float64
}

// originWeights сводит подтверждения пары из одной книги к источникам и
// возвращает вес каждого источника.
//
// Источник — цепочка подряд идущих кусков: куски нарезаны с перекрытием,
// и одна фраза из зоны перекрытия подтверждает связь дважды. Записи из ОДНОГО
// куска (A→B и B→A, два вида связи одной пары) — тоже один источник. Вес
// источника — наибольший из весов его записей, поэтому итог не зависит
// от порядка записей в журнале.
//
// До 17.09.2026 эта логика жила тремя копиями (разбиение, проекция, доктор),
// и копии разошлись: разбиение считало повтор того же куска вторым источником,
// а какая из двух записей «выживет», решала нестабильная сортировка — вес
// пары менялся от запуска к запуску (аудит, находка Б12).
func originWeights(recs []originRec) []float64 {
	if len(recs) == 0 {
		return nil
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].ord < recs[j].ord })
	out := []float64{recs[0].w}
	for i := 1; i < len(recs); i++ {
		if recs[i].ord <= recs[i-1].ord+1 {
			if recs[i].w > out[len(out)-1] {
				out[len(out)-1] = recs[i].w
			}
			continue
		}
		out = append(out, recs[i].w)
	}
	return out
}
