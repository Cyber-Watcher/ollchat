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
	s kb.Sample
	q string
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

// junkQuestion отсеивает вопросы про устройство книги, а не про её предмет.
//
// Модель иногда не распознаёт служебную страницу и добросовестно спрашивает
// «какие книги упомянуты в списке литературы». Такой вопрос меряет не поиск.
var junkQuestion = regexp.MustCompile(
	`(?i)список литератур|оглавлен|указател|на этой странице|в этой главе|` +
		`в данной книге|данного фрагмента|в этом фрагменте|в тексте|автор книги`)

func EvalGen(stdout io.Writer, cfg *config.Config, name, out string, n int, seed int64, workers int) error {
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
	samples, err := coll.SampleChunks(kb.SampleOpts{N: n, Seed: seed, SkipCode: true})
	if err != nil {
		return err
	}
	if len(samples) == 0 {
		return fmt.Errorf("в коллекции %s не нашлось подходящих кусков", name)
	}

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	ex := graphex.New(cfg.Graph.ExtractOptions(), fallback, 10*time.Minute, nil)
	if ex == nil {
		return fmt.Errorf("модель не настроена: задайте graph.model в %s", cfg.Path)
	}
	// Вопрос — обычный русский текст, синонимы для него не нужны: годится
	// та же быстрая модель, что пишет резюме тем.
	ex = ex.WithModel(cfg.Graph.SummaryModel, workers)
	if err := ex.Check(context.Background()); err != nil {
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
			q, err := askQuestion(ctx, ex, samples[i])
			mu.Lock()
			done++
			if err != nil {
				fails++
			} else {
				res[i] = evalCase{s: samples[i], q: q}
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
	skipped, junk := 0, 0
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
		default:
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return fmt.Errorf("ни одного годного вопроса не вышло")
	}

	if err := writeEvalSet(out, name, seed, ex.Model(), kept); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "набор записан: %s\n", out)
	fmt.Fprintf(stdout, "  вопросов %d, служебных страниц пропущено %d, отсеяно %d, сбоев %d\n",
		len(kept), skipped, junk, fails)
	fmt.Fprintf(stdout, "  прогон: ollchat --kb-eval %s\n", out)
	return nil
}

// askQuestion просит один вопрос по одному куску.
//
// Ответ принимается и голой строкой, и в JSON, и в ограде: замер 26.08.2026
// показал, что одна и та же модель на одном и том же промпте отвечает
// то так, то этак, и строгий разбор отбраковал все семьдесят ответов подряд.
func askQuestion(ctx context.Context, ex graph.Extractor, s kb.Sample) (string, error) {
	text := s.Text
	if len([]rune(text)) > 2500 {
		text = string([]rune(text)[:2500])
	}
	raw, err := ex.Extract(ctx, evalGenPrompt, "Фрагмент:\n"+text)
	if err != nil {
		return "", err
	}
	q := strings.TrimSpace(raw)
	if i := strings.Index(q, "{"); i >= 0 {
		var obj struct {
			Question string `json:"question"`
		}
		if json.Unmarshal([]byte(q[i:]), &obj) == nil && obj.Question != "" {
			q = obj.Question
		}
	}
	q = strings.TrimSpace(strings.Trim(strings.Split(q, "\n")[0], "`\"' "))
	if q == "" {
		return "", fmt.Errorf("пустой ответ")
	}
	if q != "ПРОПУСК" && !strings.HasSuffix(q, "?") {
		return "", fmt.Errorf("ответ не вопрос: %s", trimTitle(q))
	}
	return q, nil
}

// writeEvalSet записывает набор в TOML вместе с тем, как он собран.
//
// Условия сборки в шапке — не вежливость: без зерна и модели набор нельзя
// ни повторить, ни объяснить, а через месяц никто не вспомнит, откуда он взялся.
func writeEvalSet(path, coll string, seed int64, model string, rows []evalCase) error {
	var b strings.Builder
	fmt.Fprintf(&b, `# Замерный набор для поиска по библиотеке.
#
# Собран командой: ollchat --kb-eval-gen %s --kb-eval-gen-n %d --kb-seed %d
# Коллекция: %s. Вопросы составляла модель %s.
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
`, path, len(rows), seed, coll, model, path)

	for _, r := range rows {
		b.WriteString("\n[[case]]\n")
		fmt.Fprintf(&b, "query = %s\n", tomlString(r.q))
		fmt.Fprintf(&b, "chunk_id = %s\n", tomlString(r.s.ID))
		fmt.Fprintf(&b, "note = %s\n", tomlString(fmt.Sprintf("%s, стр. %d", r.s.Book, r.s.Page)))
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
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
