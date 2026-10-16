package kb

import (
	"math"
	"sort"
	"unicode"
)

// PRFTerms — до n слов из текстов первых кусков выдачи, самых весомых по
// TF-IDF коллекции: псевдо-релевантная обратная связь (этап 112, О8; «salient
// terms from these documents are extracted and added to the original query…
// use TF-IDF», Pai, 2025, стр. 312).
//
// **Зачем.** Вопросы у нас русские, книги чаще английские, и словесная ветка
// такой вопрос почти не находит (recall@8 0,052 на kb_answers2). Векторы язык
// переходят, поэтому первые куски СЛИЯНИЯ часто английские и по теме — их
// слова и есть мост «русский вопрос → английские слова» без модели. Слова
// дописываются только к словесной ветке; вектор вопроса не меняется.
//
// Слова возвращаются в том виде, в каком стоят в тексте (первое встреченное
// написание основы), — дописанное снова пройдёт разбор вопроса. Пропускаются
// основы самого вопроса, числа, слова короче трёх букв и основы, которые
// встречаются в коллекции меньше двух раз (опечатки и обрывки разбора).
func (c *Collection) PRFTerms(query string, texts []string, n int) []string {
	if n <= 0 || len(texts) == 0 {
		return nil
	}
	inQuery := map[string]bool{}
	for _, t := range Tokens(query, nil) {
		inQuery[t.Term] = true
	}
	tf := map[string]int{}
	surface := map[string]string{}
	var toks []Token
	for _, text := range texts {
		toks = Tokens(text, toks)
		runes := []rune(text)
		for _, t := range toks {
			if inQuery[t.Term] || !wordLike(t.Term) {
				continue
			}
			tf[t.Term]++
			if _, ok := surface[t.Term]; !ok && t.Start >= 0 && t.End <= len(runes) && t.Start < t.End {
				surface[t.Term] = string(runes[t.Start:t.End])
			}
		}
	}
	if len(tf) == 0 {
		return nil
	}

	c.mu.RLock()
	total := c.store.Count()
	type scored struct {
		term  string
		score float64
	}
	all := make([]scored, 0, len(tf))
	for term, f := range tf {
		df := 0
		for _, seg := range c.segs {
			df += seg.DF(term)
		}
		if df < 2 {
			continue
		}
		// Та же редкость, что у BM25: ln(1 + (N − df + 0,5) / (df + 0,5)).
		idf := math.Log(1 + (float64(total-df)+0.5)/(float64(df)+0.5))
		all = append(all, scored{term, float64(f) * idf})
	}
	c.mu.RUnlock()

	sort.Slice(all, func(a, b int) bool {
		if all[a].score != all[b].score {
			return all[a].score > all[b].score
		}
		return all[a].term < all[b].term
	})
	var out []string
	for _, s := range all {
		if len(out) == n {
			break
		}
		w := surface[s.term]
		if w == "" {
			w = s.term
		}
		out = append(out, w)
	}
	return out
}

// wordLike — терм похож на слово: не короче трёх знаков и не одни цифры.
func wordLike(term string) bool {
	letters, total := 0, 0
	for _, r := range term {
		total++
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return total >= 3 && letters > 0
}
