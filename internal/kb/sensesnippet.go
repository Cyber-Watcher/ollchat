package kb

// Выдержка по смыслу (этап 112, Ч21).
//
// Подмес отдаёт модели не кусок, а выдержку: `Snippet` режет окно в 320 знаков
// вокруг слов вопроса. Замер О7 (13.10.2026): при верном куске выдержка довела
// факт до ответа в 20 из 47, кусок целиком — в 31 из 47; окно по словам часто
// проходит мимо предложения с ответом. Книга (Pai, «Designing LLM Applications»,
// 2025, стр. 326) берёт в сжатие k предложений, ближайших к вопросу. Здесь то же
// реранкером, который уже стоит у поиска: один вызов на кусок, длина — та же.
// Готового смыслового выбора выдержки в коде не было (`inventory.py --find
// snippet` — только `--kb-eval-snippet`, вход реранкера, а не выдержка модели).

import (
	"context"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SnippetBySense — выдержка из предложений куска, лучших по реранкеру для
// вопроса, в пределах n знаков (0 — как у Snippet), в порядке текста; пропуск
// между взятыми предложениями помечен «…». Кусок короче n — целиком. Реранкер
// не ответил или предложение одно — возвращается fallback (выдержка по словам).
func SnippetBySense(ctx context.Context, text, query string, n int, rr Reranker, fallback string) string {
	if n <= 0 {
		n = snippetRunes
	}
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	sents := splitSentences(text)
	if len(sents) < 2 || rr == nil {
		return fallback
	}
	scores, err := rr.Rerank(ctx, query, sents)
	if err != nil || len(scores) != len(sents) {
		return fallback
	}
	order := make([]int, len(sents))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })
	keep := make([]bool, len(sents))
	used := 0
	for _, i := range order {
		l := utf8.RuneCountInString(sents[i]) + 1
		if used > 0 && used+l > n {
			continue
		}
		keep[i] = true
		used += l
		if used >= n {
			break
		}
	}
	var parts []string
	prev := -1
	for i, k := range keep {
		if !k {
			continue
		}
		if prev >= 0 && i != prev+1 {
			parts = append(parts, "…")
		}
		parts = append(parts, sents[i])
		prev = i
	}
	out := strings.Join(parts, " ")
	// Первое по оценке предложение само длиннее предела — режем по слову.
	if r := []rune(out); len(r) > n+n/4 {
		cut := string(r[:n])
		if j := strings.LastIndexByte(cut, ' '); j > n/2 {
			cut = cut[:j]
		}
		out = cut + " …"
	}
	return out
}

// splitSentences режет текст на предложения: после «.», «!», «?», «…» с пробелом
// следом и по переводу строки (строки кода и пункты списка — отдельные части).
func splitSentences(text string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			out = append(out, s)
		}
		b.Reset()
	}
	rs := []rune(text)
	for i, r := range rs {
		if r == '\n' {
			flush()
			continue
		}
		b.WriteRune(r)
		if strings.ContainsRune(".!?…", r) && (i+1 == len(rs) || unicode.IsSpace(rs[i+1])) {
			flush()
		}
	}
	flush()
	return out
}
