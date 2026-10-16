package maint

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graphex"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/qtranslate"
)

// Перевод вопроса моделью для словесной ветки (этап 112, шаг 2 плана; кандидат
// из О7, шаг 3; «перевод запроса на второй язык», docs/HowSearchWorks.md — «не
// проверено»).
//
// **Зачем.** Вопросы русские, книги чаще английские: словесная ветка находит
// по русскому вопросу 0,042–0,092 (recall@8 на kb_answers2 и kb_golden), почти
// всю полноту держат векторы. Перевод графом (--kb-eval-expand) и PRF из первых
// кусков слияния (--kb-eval-prf) слияние не подняли. Здесь английский перевод
// вопроса пишет модель и он дописывается ТОЛЬКО к словесной ветке
// (kb.SearchOpts.WordsExtra); вектор считается по русскому вопросу как есть.
//
// Переводы копятся в файле-кэше (строки JSON: вопрос и перевод), модель зовётся
// только для вопросов, которых в кэше нет: повторный прогон карту не занимает.

// translateEntry — строка файла-кэша переводов.
type translateEntry struct {
	Query string `json:"q"`
	En    string `json:"en"`
	// Ms — сколько шёл вызов модели. При одном потоке это задержка, которую
	// перевод добавит вопросу в работе; при нескольких — с ожиданием очереди.
	Ms      int64 `json:"ms,omitempty"`
	Workers int   `json:"workers,omitempty"`
}

// loadTranslations читает кэш переводов; нет файла — пустой кэш.
func loadTranslations(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("переводы %s: %w", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e translateEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Query != "" && e.En != "" {
			out[e.Query] = e.En
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("переводы %s: %w", path, err)
	}
	return out, nil
}

// translateMissing переводит вопросы набора, которых нет в кэше, и дописывает
// их в файл по одному: оборванный прогон теряет только начатые переводы.
func translateMissing(ctx context.Context, cfg *config.Config, path string, cases []kb.EvalCase,
	have map[string]string, workers int) error {
	var todo []string
	seen := map[string]bool{}
	for _, cs := range cases {
		if have[cs.Query] == "" && !seen[cs.Query] {
			seen[cs.Query] = true
			todo = append(todo, cs.Query)
		}
	}
	if len(todo) == 0 {
		return nil
	}

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	ex := graphex.New(cfg.Graph.ExtractOptions(), fallback, 10*time.Minute, nil)
	if ex == nil {
		return fmt.Errorf("модель не настроена: задайте graph.model в %s", cfg.Path)
	}
	// Перевод — короткий обычный текст: годится та же быстрая модель, что
	// пишет резюме тем и составляет вопросы набора (--kb-eval-gen).
	if workers <= 0 {
		workers = 4
	}
	ex = ex.WithModel(cfg.Graph.SummaryModel, workers)
	if err := ex.Check(ctx); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("переводы %s: %w", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)

	fmt.Fprintf(os.Stderr, "перевожу %d вопросов моделью %s (в кэше уже %d)…\n", len(todo), ex.Model(), len(have))
	start := time.Now()
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		jobs  = make(chan string)
		done  int
		first error
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for q := range jobs {
				t0 := time.Now()
				raw, err := ex.Extract(ctx, qtranslate.Prompt, q)
				ms := time.Since(t0).Milliseconds()
				en := qtranslate.Clean(raw)
				mu.Lock()
				if err != nil && first == nil {
					first = fmt.Errorf("перевод %q: %w", q, err)
				}
				if err == nil && en != "" {
					have[q] = en
					if werr := enc.Encode(translateEntry{Query: q, En: en, Ms: ms, Workers: workers}); werr != nil && first == nil {
						first = fmt.Errorf("переводы %s: %w", path, werr)
					}
				}
				done++
				if done%20 == 0 || done == len(todo) {
					fmt.Fprintf(os.Stderr, "  переведено %d из %d за %s\n", done, len(todo), time.Since(start).Round(time.Second))
				}
				mu.Unlock()
			}
		}()
	}
	for _, q := range todo {
		if ctx.Err() != nil {
			break
		}
		jobs <- q
	}
	close(jobs)
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}
