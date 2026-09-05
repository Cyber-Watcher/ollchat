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

// RankWith строит отбор подтверждений поверх источника текстов кусков.
// Источник — коллекция базы знаний; nil означает «отбирать нечем».
func RankWith(src Chunks) RankFunc {
	if src == nil {
		return nil
	}
	return func(query string, cands []ChunkKey, limit int) []ChunkKey {
		if len(cands) <= 1 || limit <= 0 {
			return head(cands, limit)
		}
		want := map[string]bool{}
		for _, t := range kb.Tokens(query, nil) {
			want[t.Term] = true
		}
		if len(want) == 0 {
			return head(cands, limit)
		}

		type scored struct {
			key   ChunkKey
			score float64
			ord   int // исходный порядок: при равенстве побеждает мнение графа
		}
		list := make([]scored, 0, len(cands))
		var buf []kb.Token
		for i, k := range cands {
			info, ok := src.ChunkByRef(k.Doc, k.Ord)
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
			return head(cands, limit)
		}
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].score != list[j].score {
				return list[i].score > list[j].score
			}
			return list[i].ord < list[j].ord
		})
		out := make([]ChunkKey, 0, limit)
		for _, s := range list {
			if len(out) >= limit {
				break
			}
			out = append(out, s.key)
		}
		return out
	}
}

func head(keys []ChunkKey, limit int) []ChunkKey {
	if limit > 0 && len(keys) > limit {
		return keys[:limit]
	}
	return keys
}
