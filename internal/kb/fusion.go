package kb

import (
	"context"
	"runtime"
	"sort"
	"sync"
)

// Поиск по смыслу и слияние с поиском по словам.
//
// Два поиска идут независимо и складываются по РАНГАМ, а не по весам:
//
//	score = Σ 1/(k + rank)
//
// Причина в том, что вес BM25 и косинус живут в разных шкалах. Любой их
// взвешенный суммой коэффициент пришлось бы подбирать под корпус и переподбирать
// после каждой смены модели эмбеддингов. Слияние по рангам не требует
// калибровки вовсе и — что важнее — вырождается в сегодняшнее поведение, когда
// векторов нет: остаётся ровно один список.
//
// Перебор полный, без приближённых структур. Четверть миллиона векторов по 1024
// байта — это 255 МБ и столько же скалярных произведений в int32; на нескольких
// горутинах счёт занимает десятки миллисекунд. HNSW добавил бы отдельную
// сложность со своими способами тихо испортить выдачу.

// rrfK задаёт, насколько быстро падает вклад дальних мест при слиянии.
//
// Чем меньше, тем важнее первые места; чем больше — тем сильнее награда за то,
// что кусок нашли оба поиска сразу.
//
// В работе, где приём предложен, стоит 60, и я взял это число не думая. На живой
// библиотеке оно оказалось вредным: кусок, средний в обоих списках, обходил
// отличный в одном. Запрос «перехват паники и восстановление» выдавал книгу
// про Elastic Stack, хотя чистый поиск по смыслу ставил первыми ровно нужные
// главы «Go на практике» и «The Anatomy of Go».
//
// 20 подобрано замером на настоящей коллекции (TestSemanticRRF): шум уходит,
// а награда за согласие списков остаётся заметной. Числа — в
// docs/kb_semantic_search.md.
var rrfK = 20.0

// SetRRFK меняет постоянную слияния. Только для замеров: в обычной работе
// значение одно и подобрано на живой коллекции.
func SetRRFK(k float64) {
	if k > 0 {
		rrfK = k
	}
}

// defaultSemanticWeight — сколько стоит мнение смыслового поиска против
// словесного. Подобран замером на живой библиотеке, см.
// docs/kb_semantic_search.md.
const defaultSemanticWeight = 1.0

// vectorCandidates — сколько кусков берём из каждого поиска до слияния.
// Больше, чем нужно на выходе: слияние должно иметь из чего выбирать.
const vectorCandidates = 200

// searchVectors ищет ближайшие по смыслу куски.
//
// Возвращает места в порядке убывания близости. Куски, у которых вектора ещё
// нет, просто не участвуют: покрытие — начальный отрезок, и это нормальное
// состояние коллекции, в которую доложили книги.
func (c *Collection) searchVectors(query []int8, limit int, allow map[uint32]bool, minCos float64) []Hit {
	if c.vectors == nil || len(query) == 0 || c.vectors.Dim() != len(query) {
		return nil
	}
	n := c.vectors.Count()
	if n > c.store.Count() {
		n = c.store.Count()
	}
	if n == 0 {
		return nil
	}
	if limit <= 0 {
		limit = vectorCandidates
	}

	workers := runtime.NumCPU()
	if workers > 12 {
		workers = 12
	}
	if workers < 1 || n < 2048 {
		workers = 1
	}

	parts := make([][]Hit, workers)
	var wg sync.WaitGroup
	step := (n + workers - 1) / workers
	for w := 0; w < workers; w++ {
		from, to := w*step, (w+1)*step
		if to > n {
			to = n
		}
		if from >= to {
			continue
		}
		wg.Add(1)
		go func(w, from, to int) {
			defer wg.Done()
			local := make([]Hit, 0, limit)
			for i := from; i < to; i++ {
				if allow != nil && !allow[c.store.Rec(i).Doc] {
					continue
				}
				cos := Cosine(c.vectors.At(i), query)
				if cos < minCos {
					continue // слабое совпадение не должно вытеснять сильное словесное
				}
				local = append(local, Hit{Chunk: i, Score: cos})
			}
			sort.Slice(local, func(a, b int) bool { return local[a].Score > local[b].Score })
			if len(local) > limit {
				local = local[:limit]
			}
			parts[w] = local
		}(w, from, to)
	}
	wg.Wait()

	var all []Hit
	for _, p := range parts {
		all = append(all, p...)
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].Score != all[b].Score {
			return all[a].Score > all[b].Score
		}
		return all[a].Chunk < all[b].Chunk
	})
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// fuse складывает списки по рангам с разными весами.
//
// Порядок внутри каждого списка — единственное, что берётся во внимание;
// сами веса BM25 и косинусы в счёт не идут. Вес списка (weights) задаёт,
// насколько его мнение важнее: на техническом корпусе точный термин надёжнее
// смыслового сходства, поэтому словесный список может стоить дороже.
//
// Списков без веса быть не должно: недостающие веса считаются единицей.
func fuse(weights []float64, lists ...[]Hit) []Hit {
	score := map[int]float64{}
	terms := map[int]int{}
	for i, list := range lists {
		w := 1.0
		if i < len(weights) && weights[i] > 0 {
			w = weights[i]
		}
		for rank, h := range list {
			score[h.Chunk] += w / (rrfK + float64(rank+1))
			if h.Terms > terms[h.Chunk] {
				terms[h.Chunk] = h.Terms
			}
		}
	}
	out := make([]Hit, 0, len(score))
	for chunk, s := range score {
		out = append(out, Hit{Chunk: chunk, Score: s, Terms: terms[chunk]})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		return out[a].Chunk < out[b].Chunk
	})
	return out
}

// embedQuery считает вектор запроса.
//
// Отдельная функция, потому что у запроса своя судьба: если сервер эмбеддингов
// недоступен, поиск обязан продолжиться по словам, а не упасть. Ошибка здесь
// не ошибка поиска, а причина обойтись без второго списка.
func embedQuery(ctx context.Context, emb Embedder, query string, dim int) ([]int8, error) {
	vecs, err := emb.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 || len(vecs[0]) != dim {
		return nil, nil
	}
	return Quantize(vecs[0]), nil
}
