package kb

// Второй векторный вид коллекции без пересчёта векторов (этап 112, С9).
//
// Книга («AI Agents in Action», Lanham, 2025, стр. 218, `books/654#687`): один документ
// режут несколькими способами сразу и получают несколько векторных видов. Честная
// вторая нарезка — переиндексация и векторы заново, часы карты. Здесь дешёвое
// приближение «крупной нарезки»: окно из куска и двух его соседей по номеру в той
// же книге, вектор окна — нормированная сумма уже посчитанных векторов (около
// трёх тысяч знаков вместо тысячи). Приём уже был у вектора книги
// (`BookCentroids` — среднее векторов кусков), здесь — среднее по окну.
//
// Это приближение: сумма векторов ≠ вектор склеенного текста. Прибавку оно может
// показать или не показать; отсутствие прибавки — повод не тратить часы карты на
// настоящую нарезку, прибавка — повод их потратить.

import (
	"context"
	"math"
	"runtime"
	"sort"
	"sync"
)

// WindowView — вид «окно из трёх кусков одной книги». Нормы окон считаются один
// раз на вид; на вопрос — одно скалярное произведение на кусок.
type WindowView struct {
	c          *Collection
	n          int
	prev, next []int32   // сосед той же книги по номеру; −1 — нет
	norm       []float64 // длина суммы векторов окна; 0 — кусок вне вида
	// Self — контроль замера: «окно» из одного куска. Отделяет пользу соседей
	// от пользы второго смыслового списка в слиянии как такового.
	Self bool
}

// NewWindowView строит вид по векторам коллекции; nil — векторов нет.
// Оглавления, списки литературы и куски удалённых книг в вид не входят —
// в поиске они тоже не участвуют. self — контроль: окно из одного куска.
func (c *Collection) NewWindowView(self bool) *WindowView {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.vectors == nil || c.store == nil {
		return nil
	}
	n := c.vectors.Count()
	if s := c.store.Count(); n > s {
		n = s
	}
	c.refOnce.Do(c.buildRefIndex)
	live := c.liveDocs()
	w := &WindowView{c: c, n: n, prev: make([]int32, n), next: make([]int32, n), norm: make([]float64, n), Self: self}
	usable := func(i int) bool {
		rec := c.store.Rec(i)
		return live[rec.Doc] && rec.Flags&uint16(FlagTOC|FlagRefs) == 0 && len(c.vectors.At(i)) > 0
	}
	ok := make([]bool, n)
	for i := 0; i < n; i++ {
		ok[i] = usable(i)
	}
	for i := 0; i < n; i++ {
		w.prev[i], w.next[i] = -1, -1
		if !ok[i] {
			continue
		}
		rec := c.store.Rec(i)
		if rec.Ord > 0 {
			if j, found := c.byRef[uint64(rec.Doc)<<32|uint64(rec.Ord-1)]; found && j < n && ok[j] {
				w.prev[i] = int32(j)
			}
		}
		if j, found := c.byRef[uint64(rec.Doc)<<32|uint64(rec.Ord+1)]; found && j < n && ok[j] {
			w.next[i] = int32(j)
		}
	}
	parallelRange(n, func(i int) {
		if !ok[i] {
			return
		}
		win := w.window(i)
		var sq float64
		for a := range win {
			for b := range win {
				sq += Cosine(c.vectors.At(win[a]), c.vectors.At(win[b]))
			}
		}
		if sq > 0 {
			w.norm[i] = math.Sqrt(sq)
		}
	})
	return w
}

// window — номера кусков окна вокруг i: сам кусок и соседи той же книги.
func (w *WindowView) window(i int) []int {
	out := []int{i}
	if w.Self {
		return out
	}
	if w.prev[i] >= 0 {
		out = append(out, int(w.prev[i]))
	}
	if w.next[i] >= 0 {
		out = append(out, int(w.next[i]))
	}
	return out
}

// Search — k кусков, чьи окна ближе всех к вопросу, ближайший первым. Вектор
// вопроса считается так же, как для поиска по кускам (кэш запросов).
func (w *WindowView) Search(ctx context.Context, emb Embedder, query string, k int) ([]Result, error) {
	if w == nil || k <= 0 {
		return nil, nil
	}
	if emb = EmbedderOrNil(emb); emb == nil {
		return nil, nil
	}
	c := w.c
	c.mu.RLock()
	defer c.mu.RUnlock()
	q, err := embedQuery(ctx, emb, query, c.vectors.Dim())
	if err != nil {
		return nil, err
	}
	sims := make([]float64, w.n)
	parallelRange(w.n, func(i int) {
		if w.norm[i] > 0 {
			sims[i] = Cosine(q, c.vectors.At(i))
		}
	})
	type scored struct {
		i int
		s float64
	}
	all := make([]scored, 0, w.n)
	for i := 0; i < w.n; i++ {
		if w.norm[i] == 0 {
			continue
		}
		var s float64
		for _, j := range w.window(i) {
			s += sims[j]
		}
		all = append(all, scored{i, s / w.norm[i]})
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].s != all[b].s {
			return all[a].s > all[b].s
		}
		return all[a].i < all[b].i
	})
	if len(all) > k {
		all = all[:k]
	}
	out := make([]Result, 0, len(all))
	for _, s := range all {
		rec := c.store.Rec(s.i)
		r := Result{ID: c.name + "/" + itoa(int(rec.Doc)) + "#" + itoa(int(rec.Ord)), Chunk: s.i,
			Score: s.s, UnitFrom: int(rec.UnitFrom), UnitTo: int(rec.UnitTo)}
		if d, found := c.book(rec.Doc); found {
			r.Book, r.Path = d.Title, d.Path
		}
		out = append(out, r)
	}
	return out, nil
}

// FuseRRF сливает два упорядоченных списка по RRF с постоянной k (0 — DefaultRRFK)
// и обрезает до want: так второй вид встаёт рядом с первым (С9).
func FuseRRF(a, b []Result, k float64, want int) []Result {
	if k <= 0 {
		k = DefaultRRFK
	}
	score := map[string]float64{}
	first := map[string]Result{}
	for _, list := range [][]Result{a, b} {
		for i, r := range list {
			score[r.ID] += 1 / (k + float64(i+1))
			if _, seen := first[r.ID]; !seen {
				first[r.ID] = r
			}
		}
	}
	out := make([]Result, 0, len(first))
	for _, r := range first {
		r.Score = score[r.ID]
		out = append(out, r)
	}
	sort.Slice(out, func(x, y int) bool {
		if out[x].Score != out[y].Score {
			return out[x].Score > out[y].Score
		}
		return out[x].ID < out[y].ID
	})
	if want > 0 && len(out) > want {
		out = out[:want]
	}
	return out
}

// parallelRange вызывает f для 0..n-1 на всех ядрах.
func parallelRange(n int, f func(i int)) {
	workers := runtime.NumCPU()
	var wg sync.WaitGroup
	step := (n + workers - 1) / workers
	for lo := 0; lo < n; lo += step {
		hi := min(lo+step, n)
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				f(i)
			}
		}(lo, hi)
	}
	wg.Wait()
}
