package kb

import (
	"fmt"
	"strings"
)

// NearChunks — до n кусков, ближайших по смыслу к куску id (тот же косинус по
// векторам bge-m3, что у смыслового поиска), кроме самого куска и его соседей
// в той же книге не дальше ordGap номеров, кроме кусков с кодом и коротких.
//
// Нужен замеру «вопросы-отвлекатели» (этап 112, Ч8): к куску A подбираются
// похожие B и C, в которых ответа нет, — вопрос к A, пользующийся словами B и C,
// проверяет, не путает ли поиск близкое с нужным. Ничего не пишет и модель не зовёт:
// только векторы, уже лежащие в коллекции.
func (c *Collection) NearChunks(id string, n, ordGap int) ([]Result, error) {
	doc, ord, err := parseChunkID(id, c.name)
	if err != nil {
		return nil, err
	}
	vec, ok := c.ChunkVectorByRef(doc, ord)
	if !ok {
		return nil, fmt.Errorf("у куска %q нет вектора", id)
	}
	// Щедрый запас: часть ближайших — соседи того же места книги, код и обрывки.
	var cands []string
	c.mu.RLock()
	hits := c.searchVectors(vec, n*60+40, nil, 0)
	for _, h := range hits {
		rec := c.store.Rec(h.Chunk)
		if rec.Doc == doc && absDiff(int(rec.Ord), int(ord)) <= ordGap {
			continue
		}
		if ChunkFlags(rec.Flags)&FlagCode != 0 {
			continue
		}
		cands = append(cands, fmt.Sprintf("%s/%d#%d", c.name, rec.Doc, rec.Ord))
	}
	c.mu.RUnlock()

	var out []Result
	for _, cid := range cands {
		if len(out) >= n {
			break
		}
		res, err := c.Around(cid, 0)
		if err != nil || len(res) == 0 {
			continue
		}
		if len(strings.Fields(res[0].Text)) < nearMinWords {
			continue
		}
		out = append(out, res[0])
	}
	return out, nil
}

// nearMinWords — короче этого кусок не годится в отвлекатели: обрывок
// не похож на «место, где мог быть ответ».
const nearMinWords = 60

func absDiff(a, b int) int {
	if a < b {
		return b - a
	}
	return a - b
}
