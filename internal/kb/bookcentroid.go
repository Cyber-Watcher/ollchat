package kb

import (
	"context"
	"math"
	"sort"
)

// Вектор книги — нормированное среднее векторов её кусков (этап 112, У2).
//
// **Зачем.** Поиск по кускам промахивается сам: на 48 вопросах О7 эталон не
// нашёлся в 39, и 38 из них нет даже в щедром списке из 50 кусков. Грубая
// ступень «в какой книге искать» сужает поле до немногих книг прежде, чем
// искать кусок. В книге («Mastering RAG», разд. 17, document summary index)
// для этого пишет резюме модель; здесь вместо резюме — среднее уже
// посчитанных векторов кусков: без модели и без карты.
//
// Пока это прибор замера (`--kb-eval-books`), в рабочем поиске не включено:
// центроид книги в сотни кусков может оказаться размытым, и исход заранее
// неизвестен.

// BookCentroids считает векторы живых книг. Оглавления и списки литературы
// не берутся — в поиске они тоже не участвуют. Книга без единого вектора
// в ответ не попадает.
func (c *Collection) BookCentroids() map[uint32][]float32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.vectors == nil {
		return nil
	}
	n := c.vectors.Count()
	if s := c.store.Count(); n > s {
		n = s
	}
	dim := c.vectors.Dim()
	live := c.liveDocs()
	sums := map[uint32][]float32{}
	for i := 0; i < n; i++ {
		rec := c.store.Rec(i)
		if !live[rec.Doc] || rec.Flags&uint16(FlagTOC|FlagRefs) != 0 {
			continue
		}
		s := sums[rec.Doc]
		if s == nil {
			s = make([]float32, dim)
			sums[rec.Doc] = s
		}
		// Вектор куска нормирован до квантования (Quantize), поэтому каждый
		// кусок входит в среднее с равным весом.
		for j, x := range c.vectors.At(i) {
			s[j] += float32(x)
		}
	}
	for _, s := range sums {
		unitize(s)
	}
	return sums
}

// BooksByCentroid — книги по близости вопроса к их векторам, ближайшая
// первой; n ≤ 0 — все. Вектор вопроса считается так же, как для поиска
// по кускам, и берётся из кэша запросов, если он там есть.
func (c *Collection) BooksByCentroid(ctx context.Context, emb Embedder, query string,
	cents map[uint32][]float32, n int) ([]uint32, error) {
	if emb = EmbedderOrNil(emb); emb == nil || len(cents) == 0 {
		return nil, nil
	}
	c.mu.RLock()
	dim := 0
	if c.vectors != nil {
		dim = c.vectors.Dim()
	}
	c.mu.RUnlock()
	if dim == 0 {
		return nil, nil
	}
	q, err := embedQuery(ctx, emb, query, dim)
	if err != nil {
		return nil, err
	}
	return rankByCentroid(q, cents, n), nil
}

// rankByCentroid упорядочивает книги по косинусу между вопросом и вектором
// книги; при равном косинусе — по номеру, чтобы порядок не зависел от обхода
// карты.
func rankByCentroid(q []int8, cents map[uint32][]float32, n int) []uint32 {
	qf := make([]float32, len(q))
	for i, x := range q {
		qf[i] = float32(x)
	}
	unitize(qf)
	type scored struct {
		doc uint32
		cos float64
	}
	all := make([]scored, 0, len(cents))
	for doc, v := range cents {
		if len(v) != len(qf) {
			continue
		}
		var dot float64
		for i := range v {
			dot += float64(v[i]) * float64(qf[i])
		}
		all = append(all, scored{doc, dot})
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].cos != all[b].cos {
			return all[a].cos > all[b].cos
		}
		return all[a].doc < all[b].doc
	})
	if n > 0 && len(all) > n {
		all = all[:n]
	}
	out := make([]uint32, len(all))
	for i, s := range all {
		out[i] = s.doc
	}
	return out
}

// unitize приводит вектор к единичной длине; нулевой оставляет как есть.
func unitize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	k := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= k
	}
}

// DocOfChunkID — номер книги из ссылки на кусок «books/98#173».
func DocOfChunkID(id string) (uint32, bool) {
	doc, _, ok := splitChunkID(id)
	if !ok || doc < 0 {
		return 0, false
	}
	return uint32(doc), true
}
