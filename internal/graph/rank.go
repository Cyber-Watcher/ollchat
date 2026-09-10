package graph

import (
	"math"
	"sort"

	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Отбор подтверждений по смыслу вопроса.
//
// Граф знает, где понятия упоминаются, но не знает, где на вопрос отвечают.
// Выбор «куска, в котором встретилось больше всего найденных понятий»
// систематически выигрывает у отзывов на обложке и оглавления: там перечислены
// все понятия книги разом. Замерено 23.08.2026 на живом графе — вопрос про
// сообщества и извлечение сущностей в GraphRAG получил страницы 2 и 6.
//
// Поэтому кандидаты от графа переранжируются по словам вопроса теми же
// правилами разбора, что и поиск по книгам: одна нормализация на всю
// программу, иначе «горутины» из вопроса не совпадут с «горутина» в тексте.

// ChunkVectors — источник, у которого есть и векторы кусков (коллекция).
type ChunkVectors interface {
	ChunkVectorByRef(doc, ord uint32) ([]int8, bool)
}

// RankWith строит отбор подтверждений поверх источника текстов кусков.
// Источник — коллекция базы знаний; nil означает «отбирать нечем».
// Только по словам вопроса; со смыслом — RankWithVector.
func RankWith(src Chunks) RankFunc { return RankWithVector(src, nil, 0) }

// RankWithVector отбирает подтверждения по словам вопроса **и по его смыслу**.
//
// **Зачем смысл.** Отбор по словам берёт из пула тот кусок, где совпало больше
// слов вопроса. По английскому вопросу «configuring CoreDNS» русский кусок
// про Corefile содержит одно слово из двух, любой английский — оба, и перевод
// книги не попадал в подтверждения ни разу (замер 07.09.2026,
// замер входа этапа 98), хотя в пуле кандидатов был. Векторы кусков лежат
// в коллекции (та же bge-m3, что у вектора вопроса), и косинус с вопросом
// видит смысл сквозь язык. Он же опускает оглавления и обложки: слова там
// совпадают все, смысла — нет.
//
// Два порядка — по словам и по близости — сливаются по местам, тем же
// приёмом, что слова и векторы в поиске по книгам (kb.DefaultRRFK), и с тем же
// весом смысла: weight — kb.semantic_weight, ноль — kb.DefaultSemanticWeight.
// При равном весе оглавление (все слова, смысла нет) и перевод (смысл есть,
// слов нет) набирают поровну, и решал бы порядок графа. Кусок без вектора
// участвует одним словесным порядком: долитые книги ждут --kb-embed, и молча
// выбрасывать их из подтверждений нельзя. Ничья — по порядку графа.
// Пустой qv или источник без векторов — прежний отбор по словам.
func RankWithVector(src Chunks, qv []int8, weight float64) RankFunc {
	if src == nil {
		return nil
	}
	if weight <= 0 {
		weight = kb.DefaultSemanticWeight
	}
	vecs, _ := src.(ChunkVectors)
	return func(query string, cands []ChunkKey, limit int) []ChunkKey {
		if len(cands) <= 1 || limit <= 0 {
			return head(cands, limit)
		}
		texts := readTexts(src, cands)
		// Оглавления и указатели в подтверждения не идут (решение владельца
		// 07.09.2026): в них названы все понятия книги разом, и по словам,
		// и по смыслу они близки к короткому вопросу, а не отвечают на него.
		// Остаются они только тогда, когда кроме них не осталось ничего.
		cands = withoutTOC(cands, texts)
		byWords := wordOrder(texts, query, cands)
		var byCos []ChunkKey
		if vecs != nil && len(qv) > 0 {
			byCos = cosineOrder(vecs, qv, cands)
		}
		switch {
		case byWords == nil && byCos == nil:
			return head(cands, limit)
		case byCos == nil:
			return head(byWords, limit)
		case byWords == nil:
			return head(withRest(byCos, cands), limit)
		}
		return head(fuseRanks(cands, byWords, byCos, weight), limit)
	}
}

// readTexts читает тексты кандидатов один раз: их читают и отбор по словам,
// и отсев оглавлений.
func readTexts(src Chunks, cands []ChunkKey) map[uint64]kb.ChunkInfo {
	texts := make(map[uint64]kb.ChunkInfo, len(cands))
	for _, k := range cands {
		if info, ok := src.ChunkByRef(k.Doc, k.Ord); ok {
			texts[k.Pack()] = info
		}
	}
	return texts
}

// withoutTOC убирает кандидатов-оглавления: по признаку индекса (FlagTOC,
// этап 99), а у коллекций без прохода --kb-flag-toc — по той же эвристике
// на тексте. Если оглавления все — оставляет как есть: пустые подтверждения
// хуже плохих.
func withoutTOC(cands []ChunkKey, texts map[uint64]kb.ChunkInfo) []ChunkKey {
	out := make([]ChunkKey, 0, len(cands))
	for _, k := range cands {
		if info, ok := texts[k.Pack()]; ok && (info.TOC || kb.LooksLikeTOC(info.Text)) {
			continue
		}
		out = append(out, k)
	}
	if len(out) == 0 {
		return cands
	}
	return out
}

// wordOrder — кандидаты по убыванию совпадения со словами вопроса; nil, если
// в вопросе нет слов или ни один кусок не прочитался. При равенстве — порядок
// графа: там побеждает кусок, где найденных понятий больше.
func wordOrder(texts map[uint64]kb.ChunkInfo, query string, cands []ChunkKey) []ChunkKey {
	want := map[string]bool{}
	for _, t := range kb.Tokens(query, nil) {
		want[t.Term] = true
	}
	if len(want) == 0 {
		return nil
	}
	type scored struct {
		key   ChunkKey
		score float64
		ord   int
	}
	list := make([]scored, 0, len(cands))
	var buf []kb.Token
	for i, k := range cands {
		info, ok := texts[k.Pack()]
		if !ok {
			continue
		}
		buf = kb.Tokens(info.Text, buf)
		seen := map[string]int{}
		for _, t := range buf {
			if want[t.Term] {
				seen[t.Term]++
			}
		}
		var score float64
		for _, n := range seen {
			// Логарифм, а не число вхождений: кусок, где слово повторено
			// двадцать раз, отвечает на вопрос не в двадцать раз лучше.
			score += 1 + math.Log(float64(n))
		}
		// Доля покрытых слов вопроса весомее их частоты: кусок, где есть
		// оба понятия вопроса, ценнее того, где одно, но много раз.
		score *= 1 + float64(len(seen))/float64(len(want))
		list = append(list, scored{k, score, i})
	}
	if len(list) == 0 {
		return nil
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].ord < list[j].ord
	})
	out := make([]ChunkKey, 0, len(list))
	for _, s := range list {
		out = append(out, s.key)
	}
	return out
}

// cosineOrder — кандидаты с вектором по убыванию близости к вопросу; nil,
// если вектора нет ни у одного. Ничья — по порядку графа.
func cosineOrder(vecs ChunkVectors, qv []int8, cands []ChunkKey) []ChunkKey {
	type scored struct {
		key ChunkKey
		cos float64
		ord int
	}
	var list []scored
	for i, k := range cands {
		v, ok := vecs.ChunkVectorByRef(k.Doc, k.Ord)
		if !ok || len(v) != len(qv) {
			continue
		}
		list = append(list, scored{k, kb.Cosine(qv, v), i})
	}
	if len(list) == 0 {
		return nil
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].cos != list[j].cos {
			return list[i].cos > list[j].cos
		}
		return list[i].ord < list[j].ord
	})
	out := make([]ChunkKey, 0, len(list))
	for _, s := range list {
		out = append(out, s.key)
	}
	return out
}

// fuseRanks сливает порядок по словам и порядок по смыслу по местам (RRF):
// вклад места m — 1/(k+m), у смысла умноженный на weight. Кандидат, которого
// нет в одном из порядков, получает вклад только другого. Ничья — по порядку
// графа (cands).
func fuseRanks(cands []ChunkKey, byWords, byCos []ChunkKey, weight float64) []ChunkKey {
	const k = kb.DefaultRRFK
	score := make(map[uint64]float64, len(cands))
	for m, key := range byWords {
		score[key.Pack()] += 1 / (k + float64(m+1))
	}
	for m, key := range byCos {
		score[key.Pack()] += weight / (k + float64(m+1))
	}
	idx := make([]int, 0, len(cands))
	for i, c := range cands {
		if _, ok := score[c.Pack()]; ok {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		sa, sb := score[cands[idx[a]].Pack()], score[cands[idx[b]].Pack()]
		if sa != sb {
			return sa > sb
		}
		return idx[a] < idx[b]
	})
	out := make([]ChunkKey, 0, len(idx))
	for _, i := range idx {
		out = append(out, cands[i])
	}
	return out
}

// withRest дописывает к порядку кандидатов, которых в нём нет, — в порядке графа.
func withRest(order, cands []ChunkKey) []ChunkKey {
	seen := make(map[uint64]bool, len(order))
	for _, k := range order {
		seen[k.Pack()] = true
	}
	out := append([]ChunkKey(nil), order...)
	for _, c := range cands {
		if !seen[c.Pack()] {
			out = append(out, c)
		}
	}
	return out
}

func head(keys []ChunkKey, limit int) []ChunkKey {
	if limit > 0 && len(keys) > limit {
		return keys[:limit]
	}
	return keys
}
