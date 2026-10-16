package maint

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/graphex"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Сборка замерного набора: слепая выборка кусков и вопрос к каждому.
//
// Одной командой, а не выборкой отдельно и вопросами отдельно. Разрывать
// незачем: половина набора без второй половины бесполезна, а два шага
// в двух программах разъезжаются — у одной зерно случайности одно, у другой
// другое, и замер перестаёт повторяться.
//
// **Слепота выборки принципиальна.** Отбирать куски поиском нельзя: набор
// подсунет ровно то, что поиск и так находит. Здесь равномерная выборка
// по всей коллекции (kb.SampleChunks), и вопрос составляется по куску, а не
// по выдаче.

// evalCase — кусок и вопрос к нему.
type evalCase struct {
	s    kb.Sample
	q    string
	must string // факт из куска, который обязан прозвучать в ответе; пусто — набор только для поиска
}

// evalGenPrompt — как просить вопрос.
//
// Требования не украшение: без них модель пишет вопрос, дословно повторяющий
// фразу из куска, и такой вопрос меряет совпадение строк, а не поиск.
const evalGenPrompt = `Ты составляешь вопросы для проверки поиска по технической библиотеке.

Тебе дают фрагмент из книги. Придумай ОДИН вопрос по-русски, на который этот
фрагмент отвечает по существу.

Требования к вопросу:
- его задаёт человек, который книгу не читал и этого фрагмента не видел;
- он про смысл, а не про формулировку: не переписывай фразы из фрагмента;
- не упоминай редких имён собственных, встречающихся только в этом фрагменте;
- 4-12 слов, без кавычек, заканчивается вопросительным знаком.

Если фрагмент — оглавление, предметный указатель, список литературы, обрывок
кода без пояснений или иная служебная страница, ответь одним словом: ПРОПУСК

Ответь одной строкой: сам вопрос, без пояснений и без разметки.`

// evalGenMustPrompt — вопрос вместе с фактом, который обязан прозвучать в ответе.
//
// Нужен замерам, где отвечает модель (этап 112, О7, «потолок поиска»;
// privatescripts/kbanswers.py): ответ проверяется подстрокой, без судьи-модели,
// а судья предпочитает второй ответ в 85 % случаев (замер 26.08.2026).
// Факт просится из того, что не переводится, — ответ модели русский, а книги
// чаще английские, и переведённый термин подстрокой не найдётся.
const evalGenMustPrompt = `Ты составляешь вопросы для проверки ответов по технической библиотеке.

Тебе дают фрагмент из книги. Придумай ОДИН вопрос по-русски, на который этот
фрагмент отвечает по существу, и выпиши из фрагмента ФАКТ — то, что обязан
назвать верный ответ на этот вопрос.

Требования к вопросу:
- его задаёт человек, который книгу не читал и этого фрагмента не видел;
- он про смысл, а не про формулировку: не переписывай фразы из фрагмента;
- не упоминай редких имён собственных, встречающихся только в этом фрагменте;
- 4-12 слов, заканчивается вопросительным знаком.

Требования к факту:
- скопирован из фрагмента ДОСЛОВНО, 1-4 слова, без запятых и кавычек;
- то, что не переводится: имя команды, функции, параметра или инструмента,
  число с единицей, аббревиатура, название стандарта;
- без него ответ неверен или неполон;
- его нет в самом вопросе.

Если фрагмент — оглавление, предметный указатель, список литературы, обрывок
кода без пояснений, иная служебная страница или в нём нет такого факта,
ответь одним словом: ПРОПУСК

Иначе ответь одной строкой JSON: {"question": "вопрос?", "must": "факт"}`

// junkQuestion отсеивает вопросы про устройство книги, а не про её предмет.
//
// Модель иногда не распознаёт служебную страницу и добросовестно спрашивает
// «какие книги упомянуты в списке литературы». Такой вопрос меряет не поиск.
var junkQuestion = regexp.MustCompile(
	`(?i)список литератур|оглавлен|указател|на этой странице|в этой главе|` +
		`в данной книге|данного фрагмента|в этом фрагменте|в тексте|автор книги`)

// EvalGen собирает набор; must — просить к каждому вопросу ещё и факт для
// проверки ответа модели (поле `must`, --kb-eval-gen-must); onlyCode — брать
// только куски-код (--kb-eval-gen-code, этап 112, Ф6: в обычных наборах кода нет).
func EvalGen(stdout io.Writer, cfg *config.Config, name, out string, n int, seed int64, workers int, must, onlyCode bool) error {
	if out == "" {
		return fmt.Errorf("не задан файл набора")
	}
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("файл %s уже есть — уберите его или выберите другое имя", out)
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

	if seed == 0 {
		// Ноль означал бы «как получится», а набор обязан повторяться.
		seed = time.Now().Unix()
		fmt.Fprintf(stdout, "зерно не задано, взято %d — запишите его, если набор понадобится повторить\n", seed)
	}
	samples, err := coll.SampleChunks(kb.SampleOpts{N: n, Seed: seed, SkipCode: !onlyCode, OnlyCode: onlyCode})
	if err != nil {
		return err
	}
	if len(samples) == 0 {
		return fmt.Errorf("в коллекции %s не нашлось подходящих кусков", name)
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
	fmt.Fprintf(stdout, "коллекция %s, кусков отобрано %d, вопросы составляет %s\n",
		name, len(samples), ex.Model())

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		jobs  = make(chan int)
		res   = make([]evalCase, len(samples))
		done  int
		last  = time.Now()
		fails int
	)
	worker := func() {
		defer wg.Done()
		for i := range jobs {
			if ctx.Err() != nil {
				return
			}
			q, fact, err := askQuestion(ctx, ex, samples[i], must)
			mu.Lock()
			done++
			if err != nil {
				fails++
			} else {
				res[i] = evalCase{s: samples[i], q: q, must: fact}
			}
			if time.Since(last) > 2*time.Second || done == len(samples) {
				last = time.Now()
				fmt.Fprintf(os.Stderr, "\r  %d/%d · сбоев %d   ", done, len(samples), fails)
			}
			mu.Unlock()
		}
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go worker()
	}
	for i := range samples {
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	fmt.Fprintln(os.Stderr)

	var kept []evalCase
	skipped, junk, noFact := 0, 0, 0
	for _, r := range res {
		switch {
		case r.q == "":
			continue
		case r.q == "ПРОПУСК":
			skipped++
		case junkQuestion.MatchString(r.q):
			junk++
		case len(strings.Fields(r.q)) < 4 || len(strings.Fields(r.q)) > 16:
			junk++
		case must && badMust(r.must, r.q, r.s.Text) != "":
			noFact++
		default:
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return fmt.Errorf("ни одного годного вопроса не вышло")
	}

	if err := writeEvalSet(out, name, seed, ex.Model(), kept, must, onlyCode); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "набор записан: %s\n", out)
	fmt.Fprintf(stdout, "  вопросов %d, служебных страниц пропущено %d, отсеяно %d, сбоев %d\n",
		len(kept), skipped, junk, fails)
	if must {
		fmt.Fprintf(stdout, "  отброшено за негодный факт (нет в куске дословно, есть в вопросе, длинный): %d\n", noFact)
		fmt.Fprintf(stdout, "  прогон: python3 privatescripts/kbanswers.py --set %s\n", out)
		return nil
	}
	fmt.Fprintf(stdout, "  прогон: ollchat --kb-eval %s\n", out)
	return nil
}

// genExtractor — модель, что пишет вопросы к наборам: быстрая модель резюме тем,
// проверенная до первого запроса.
func genExtractor(cfg *config.Config, workers int) (graph.Extractor, error) {
	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	ex := graphex.New(cfg.Graph.ExtractOptions(), fallback, 10*time.Minute, nil)
	if ex == nil {
		return nil, fmt.Errorf("модель не настроена: задайте graph.model в %s", cfg.Path)
	}
	// Вопрос — обычный русский текст, синонимы для него не нужны: годится
	// та же быстрая модель, что пишет резюме тем.
	ex = ex.WithModel(cfg.Graph.SummaryModel, workers)
	if err := ex.Check(context.Background()); err != nil {
		return nil, err
	}
	return ex, nil
}

// badMust — чем негоден факт для проверки ответа; пусто — годен.
//
// Проверка кодом, а не верой в модель: факт, которого нет в куске дословно,
// меряет память модели, а не то, дошёл ли кусок до ответа; факт из самого
// вопроса засчитывается любому ответу, повторившему вопрос. Запятая и кавычки
// запрещены форматом: kbanswers.py делит список `must` по запятым.
func badMust(must, question, chunk string) string {
	must = strings.TrimSpace(must)
	switch {
	case must == "":
		return "факта нет"
	case strings.ContainsAny(must, `,"[]`):
		return "запятая или кавычки"
	case len([]rune(must)) < 3 || len([]rune(must)) > 40:
		return "длина не 3–40 знаков"
	case len(strings.Fields(must)) > 4:
		return "длиннее четырёх слов"
	}
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	if !strings.Contains(norm(chunk), norm(must)) {
		return "нет в куске дословно"
	}
	if strings.Contains(norm(question), norm(must)) {
		return "есть в самом вопросе"
	}
	return ""
}

// askQuestion просит один вопрос по одному куску.
//
// Ответ принимается и голой строкой, и в JSON, и в ограде: замер 26.08.2026
// показал, что одна и та же модель на одном и том же промпте отвечает
// то так, то этак, и строгий разбор отбраковал все семьдесят ответов подряд.
//
// С must модель отвечает JSON с вопросом и фактом; годность факта проверяет
// badMust уже после, чтобы отброшенные были видны числом в итоге.
func askQuestion(ctx context.Context, ex graph.Extractor, s kb.Sample, must bool) (string, string, error) {
	text := s.Text
	if len([]rune(text)) > 2500 {
		text = string([]rune(text)[:2500])
	}
	prompt := evalGenPrompt
	if must {
		prompt = evalGenMustPrompt
	}
	raw, err := ex.Extract(ctx, prompt, "Фрагмент:\n"+text)
	if err != nil {
		return "", "", err
	}
	return parseGenAnswer(raw)
}

// parseGenAnswer разбирает ответ модели: голая строка, JSON или JSON в ограде.
func parseGenAnswer(raw string) (string, string, error) {
	q, fact := strings.TrimSpace(raw), ""
	if i := strings.Index(q, "{"); i >= 0 {
		var obj struct {
			Question string `json:"question"`
			Must     string `json:"must"`
		}
		body := q[i:]
		if j := strings.LastIndex(body, "}"); j >= 0 {
			body = body[:j+1]
		}
		if json.Unmarshal([]byte(body), &obj) == nil && obj.Question != "" {
			q, fact = obj.Question, strings.TrimSpace(obj.Must)
		}
	}
	q = strings.TrimSpace(strings.Trim(strings.Split(q, "\n")[0], "`\"' "))
	if q == "" {
		return "", "", fmt.Errorf("пустой ответ")
	}
	if q != "ПРОПУСК" && !strings.HasSuffix(q, "?") {
		return "", "", fmt.Errorf("ответ не вопрос: %s", trimTitle(q))
	}
	return q, fact, nil
}

// writeEvalSet записывает набор в TOML вместе с тем, как он собран.
//
// Условия сборки в шапке — не вежливость: без зерна и модели набор нельзя
// ни повторить, ни объяснить, а через месяц никто не вспомнит, откуда он взялся.
func writeEvalSet(path, coll string, seed int64, model string, rows []evalCase, must, onlyCode bool) error {
	var b strings.Builder
	mustFlag := ""
	if must {
		mustFlag = " --kb-eval-gen-must"
	}
	if onlyCode {
		mustFlag += " --kb-eval-gen-code"
	}
	fmt.Fprintf(&b, `# Замерный набор для поиска по библиотеке.
#
# Собран командой: ollchat --kb-eval-gen %s --kb-eval-gen-n %d --kb-seed %d%s
# Коллекция: %s. Вопросы составляла модель %s.`, path, len(rows), seed, mustFlag, coll, model)
	if must {
		b.WriteString(`
#
# Поле must — факт, который модель выписала из куска ДОСЛОВНО (проверено кодом:
# он есть в куске и его нет в вопросе). Им kbanswers.py проверяет ответ модели
# подстрокой, без судьи. Число в шапке --kb-eval-gen-n — сколько вопросов
# осталось после отсева, а не сколько кусков отбиралось.`)
	}
	fmt.Fprintf(&b, `
#
# Куски отобраны ВСЛЕПУЮ, равномерной выборкой по всей коллекции: отбирать их
# поиском нельзя, иначе набор подсунет ровно то, что поиск и так находит,
# и замер подтвердит сам себя.
#
# Правильным ответом считается ровно тот кусок, по которому вопрос составлен.
# Это делает замер пессимистичным: на общий вопрос в библиотеке отвечают
# десятки кусков, а засчитывается один. Для СРАВНЕНИЯ способов искать это
# не мешает — занижение одинаково для всех.
#
# Прогон: ollchat --kb-eval %s
`, path)
	if must {
		fmt.Fprintf(&b, "# Ответы модели: python3 privatescripts/kbanswers.py --set %s\n", path)
	}

	writeEvalCases(&b, rows, must)
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// writeEvalCases дописывает блоки `[[case]]` — общая часть всех наборов,
// которые собирает этот файл.
func writeEvalCases(b *strings.Builder, rows []evalCase, must bool) {
	for _, r := range rows {
		b.WriteString("\n[[case]]\n")
		fmt.Fprintf(b, "query = %s\n", tomlString(r.q))
		fmt.Fprintf(b, "chunk_id = %s\n", tomlString(r.s.ID))
		if must {
			fmt.Fprintf(b, "must = [%s]\n", tomlString(r.must))
		}
		fmt.Fprintf(b, "note = %s\n", tomlString(fmt.Sprintf("%s, стр. %d", r.s.Book, r.s.Page)))
	}
}

// tomlString — строка в кавычках с обычным экранированием.
func tomlString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return `"` + s + `"`
}

// Sample печатает слепую выборку кусков в JSON.
//
// Нужна, когда вопросы к набору пишут не моделью, а руками: выборка обязана
// остаться слепой, а всё остальное — дело человека.
func Sample(stdout io.Writer, cfg *config.Config, name string, n int, seed int64) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	if seed == 0 {
		seed = time.Now().Unix()
		fmt.Fprintf(os.Stderr, "зерно не задано, взято %d\n", seed)
	}
	samples, err := coll.SampleChunks(kb.SampleOpts{N: n, Seed: seed, SkipCode: true})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "отобрано кусков: %d (зерно %d)\n", len(samples), seed)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	return enc.Encode(samples)
}
