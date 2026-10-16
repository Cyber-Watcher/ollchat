package maint

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/BurntSushi/toml"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Набор вопросов-«отвлекателей» (этап 112, Ч8).
//
// Набор kb_golden собран по одному куску на вопрос и слишком лёгок: вопрос
// повторяет слова своего куска, и поиск находит его по словам. Книга описывает
// другой вопрос: к куску A подбирают похожие B и C без ответа и просят вопрос,
// «only answered by Chunk A, but which reuses terminology or themes from B or C»
// («Hands-On RAG for Production», 2026, `718#379`; по отчёту субагента).
//
// Здесь куски A берутся из готового набора с фактом `must` (kb_answers2.toml),
// поэтому вопросы старого и нового наборов парные: один и тот же кусок, обычный
// вопрос и вопрос-отвлекатель. Разность recall@8 на них — цена отвлекателей.
// B и C — ближайшие по вектору куски коллекции (`kb.NearChunks`), не соседи A
// по книге. Факт `must` берётся из A и обязан отсутствовать в B и C: иначе ответ
// есть и там, и проверка строкой неверна.

// evalGenDistractPrompt — как просить трудный вопрос.
const evalGenDistractPrompt = `Ты составляешь ТРУДНЫЕ вопросы для проверки поиска по технической библиотеке.

Тебе дают три фрагмента из книг. Фрагмент A — тот, по которому составляется вопрос.
Фрагменты B и C похожи на него по теме, но ответа на этот вопрос в них НЕТ.

Придумай ОДИН вопрос по-русски, на который отвечает ТОЛЬКО фрагмент A, но который
пользуется словами или темами фрагментов B и C — так, чтобы поиск по словам вопроса
легко принёс B или C вместо A. Выпиши из фрагмента A ФАКТ — то, что обязан назвать
верный ответ.

Требования к вопросу:
- его задаёт человек, который книг не читал и этих фрагментов не видел;
- он про смысл, а не про формулировку: не переписывай фразы из фрагментов;
- 4-12 слов, заканчивается вопросительным знаком.

Требования к факту:
- скопирован из фрагмента A ДОСЛОВНО, 1-4 слова, без запятых и кавычек;
- то, что не переводится: имя команды, функции, параметра или инструмента,
  число с единицей, аббревиатура, название стандарта;
- в фрагментах B и C его НЕТ, в самом вопросе его нет.

Если фрагмент A — оглавление, указатель, список литературы, обрывок кода или в нём
нет такого факта, ответь одним словом: ПРОПУСК

Иначе ответь одной строкой JSON: {"question": "вопрос?", "must": "факт"}`

// distractExcerpt — сколько знаков каждого фрагмента идёт в модель: A короче
// не бывает смысла, B и C нужны для темы, а не целиком.
const (
	distractMaxA = 2500
	distractMaxB = 1200
)

// nearOrdGap — отвлекателем не считается кусок той же книги ближе стольких номеров.
const nearOrdGap = 3

// distractOK — годится ли пара «вопрос, факт» к куску A при отвлекателях others:
// отказ — строка с причиной, пусто — годна. Проверки кодом, а не верой в модель.
func distractOK(q, must, textA string, others []string) string {
	switch {
	case q == "ПРОПУСК":
		return "служебный кусок"
	case junkQuestion.MatchString(q):
		return "вопрос про устройство книги"
	case len(strings.Fields(q)) < 4 || len(strings.Fields(q)) > 16:
		return "длина вопроса"
	}
	if why := badMust(must, q, textA); why != "" {
		return why
	}
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	for _, o := range others {
		if strings.Contains(norm(o), norm(must)) {
			return "факт есть и в отвлекателе"
		}
	}
	return ""
}

// cut обрезает строку до n рун.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// EvalGenDistract собирает набор отвлекателей к кускам набора src.
func EvalGenDistract(stdout io.Writer, cfg *config.Config, name, src, out string, workers int) error {
	if out == "" || src == "" {
		return fmt.Errorf("нужны оба файла: исходный набор (--kb-eval-gen-distract) и новый (--kb-eval-gen)")
	}
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("файл %s уже есть — уберите его или выберите другое имя", out)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var f evalFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("набор %s не читается: %w", src, err)
	}
	var ids []string
	for _, c := range f.Case {
		if c.ChunkID != "" {
			ids = append(ids, c.ChunkID)
		}
	}
	if len(ids) == 0 {
		return fmt.Errorf("в наборе %s нет вопросов со ссылкой на кусок", src)
	}

	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	ex, err := genExtractor(cfg, workers)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if workers <= 0 {
		workers = 4
	}
	fmt.Fprintf(stdout, "коллекция %s, кусков в наборе %s: %d, отвлекатели подбираются по векторам, вопросы составляет %s\n",
		name, src, len(ids), ex.Model())

	type job struct {
		a      kb.Result
		others []kb.Result
	}
	jobs := make([]job, 0, len(ids))
	noNear := 0
	for _, id := range ids {
		res, err := coll.Around(id, 0)
		if err != nil || len(res) == 0 {
			noNear++
			continue
		}
		near, err := coll.NearChunks(id, 2, nearOrdGap)
		if err != nil || len(near) < 2 {
			noNear++
			continue
		}
		jobs = append(jobs, job{a: res[0], others: near})
	}
	fmt.Fprintf(stdout, "  куски с двумя отвлекателями: %d из %d (без них %d)\n", len(jobs), len(ids), noNear)

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		queue   = make(chan int)
		res     = make([]evalCase, len(jobs))
		why     = make(map[string]int)
		fails   int
		doneNum int
	)
	worker := func() {
		defer wg.Done()
		for i := range queue {
			if ctx.Err() != nil {
				return
			}
			j := jobs[i]
			input := "Фрагмент A:\n" + cutRunes(j.a.Text, distractMaxA) +
				"\n\nФрагмент B:\n" + cutRunes(j.others[0].Text, distractMaxB) +
				"\n\nФрагмент C:\n" + cutRunes(j.others[1].Text, distractMaxB)
			raw, err := ex.Extract(ctx, evalGenDistractPrompt, input)
			var q, must string
			if err == nil {
				q, must, err = parseGenAnswer(raw)
			}
			mu.Lock()
			doneNum++
			switch {
			case err != nil:
				fails++
			default:
				if bad := distractOK(q, must, j.a.Text, []string{j.others[0].Text, j.others[1].Text}); bad != "" {
					why[bad]++
				} else {
					res[i] = evalCase{
						s: kb.Sample{ID: j.a.ID, Book: j.a.Book, Page: j.a.UnitFrom, Year: j.a.Year, Text: j.a.Text},
						q: q, must: must,
					}
				}
			}
			fmt.Fprintf(os.Stderr, "\r  %d/%d · сбоев %d   ", doneNum, len(jobs), fails)
			mu.Unlock()
		}
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go worker()
	}
	for i := range jobs {
		select {
		case queue <- i:
		case <-ctx.Done():
		}
	}
	close(queue)
	wg.Wait()
	fmt.Fprintln(os.Stderr)

	var kept []evalCase
	for _, r := range res {
		if r.q != "" {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return fmt.Errorf("ни одного годного вопроса не вышло (отказы: %v, сбоев %d)", why, fails)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `# Набор вопросов-отвлекателей (этап 112, Ч8).
#
# Собран командой: ollchat --kb-eval-gen %s --kb-eval-gen-distract %s
# Коллекция: %s. Вопросы составляла модель %s.
#
# Куски A — те же, что в наборе %s: вопросы парные (тот же кусок, другой
# вопрос). К каждому подобраны два ближайших по вектору куска B и C из других
# мест коллекции; вопрос пользуется их словами, а ответ даёт только A.
# Факт must — дословно из A и отсутствует в B, C и вопросе (проверено кодом).
#
# Прогон: ollchat --kb-eval %s
# Ответы модели: python3 privatescripts/kbanswers.py --set %s
`, out, src, name, ex.Model(), src, out, out)
	writeEvalCases(&b, kept, true)
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "набор записан: %s\n", out)
	fmt.Fprintf(stdout, "  вопросов %d из %d кусков, сбоев %d\n", len(kept), len(jobs), fails)
	for reason, n := range why {
		fmt.Fprintf(stdout, "  отсеяно: %s — %d\n", reason, n)
	}
	return nil
}
