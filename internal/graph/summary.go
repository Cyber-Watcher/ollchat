package graph

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Резюме сообществ: короткое описание каждой темы, написанное моделью.
//
// Цена вопроса. Извлечение сущностей — это запрос на каждый кусок книги,
// сотни тысяч запросов и недели работы карты. Резюме — один запрос
// на сообщество, то есть сотни запросов и часы. Разница в три порядка,
// и именно она делает этап осмысленным.
//
// Что модель получает: имена понятий сообщества по убыванию упоминаний
// и самые весомые связи между ними. Цитат не даётся намеренно — резюме
// описывает **тему**, а не пересказывает книгу; за цитатами есть kb_search.
//
// Что модель возвращает: заголовок, два-три предложения описания и список
// ключевых понятий. Строгий JSON, как при извлечении: разбор ответа не должен
// зависеть от того, в каком настроении модель расставила абзацы.

// SummaryPrompt — системное сообщение для резюме сообщества.
//
//go:embed prompts/summary.txt
var SummaryPrompt string

// SummaryOpts — как строить резюме.
type SummaryOpts struct {
	// MinMembers — сообщества меньше этого размера пропускаются: тема
	// из двух понятий темой не является, а запрос стоит столько же.
	MinMembers int

	// MaxMembers — сколько понятий показывать модели. Всё сообщество целиком
	// не нужно: первые по упоминаниям и определяют тему, а хвост только
	// раздувает запрос.
	MaxMembers int

	// MaxRelations — сколько связей показывать.
	MaxRelations int

	// Levels — какие уровни размечать. Пусто — только нулевой: объединения
	// верхнего уровня осмысленно называть уже по резюме вложенных.
	Levels []int

	// Workers — сколько тем описывать одновременно. 0 — четыре: столько же,
	// сколько при извлечении, и по той же причине — карта не успевает
	// простаивать между запросами.
	Workers int
}

func (o SummaryOpts) norm() SummaryOpts {
	if o.MinMembers <= 0 {
		o.MinMembers = 5
	}
	if o.MaxMembers <= 0 {
		o.MaxMembers = 40
	}
	if o.MaxRelations <= 0 {
		o.MaxRelations = 30
	}
	if len(o.Levels) == 0 {
		o.Levels = []int{0}
	}
	if o.Workers <= 0 {
		o.Workers = 4
	}
	return o
}

// SummaryProgress — ход работы.
type SummaryProgress struct {
	Total   int
	Done    int
	Skipped int
	Failed  int
	Title   string // последнее полученное название
	Elapsed time.Duration
}

// Summarize просит модель назвать и описать каждое сообщество.
//
// Прерывание безопасно: резюме записываются на диск пачками, и повторный
// запуск берётся за те сообщества, у которых заголовка ещё нет.
func (g *Graph) Summarize(ctx context.Context, ex Extractor, c *Communities,
	opt SummaryOpts, report func(SummaryProgress)) error {

	opt = opt.norm()
	levels := map[int]bool{}
	for _, l := range opt.Levels {
		levels[l] = true
	}

	var work []int
	for i, com := range c.List {
		if !levels[com.Level] || com.Title != "" {
			continue
		}
		if len(com.Members) < opt.MinMembers {
			continue
		}
		work = append(work, i)
	}

	started := time.Now()
	pr := SummaryProgress{Total: len(work)}

	// Работа идёт в несколько потоков.
	//
	// Замер 26.08.2026 на живом прогоне в один поток: карта стенда загружена
	// на 50–62% и мощность 100–160 Вт из 400, тогда как при четырёх потоках
	// извлечения держалось 88%. Пила на графике видна отчётливо — между
	// ответом и следующим запросом карта простаивает, пока эта машина
	// разбирает JSON и собирает следующий запрос. Потоки эту паузу
	// заполняют.
	//
	// Запись в разбиение идёт только из этого цикла, под замком: сообщества
	// разные, но срез общий, а сохранение на диск обязано видеть его целиком.
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		jobs = make(chan int)
	)
	done := 0
	worker := func() {
		defer wg.Done()
		for i := range jobs {
			if ctx.Err() != nil {
				return
			}
			mu.Lock()
			com := c.List[i]
			mu.Unlock()

			out, err := g.askSummary(ctx, ex, &com, opt)

			mu.Lock()
			switch {
			case err != nil && ctx.Err() != nil:
				mu.Unlock()
				return
			case err != nil:
				pr.Failed++
			default:
				c.List[i].Title = out.Title
				c.List[i].Summary = out.Summary
				c.List[i].Key = out.Key
				c.List[i].Rating = out.Rating
				c.List[i].Why = out.Why
				c.List[i].Books = g.booksOfCommunity(&c.List[i])
				pr.Done++
				pr.Title = out.Title
			}
			done++
			pr.Elapsed = time.Since(started)
			snapshot := pr
			// Запись пачками: терять при обрыве весь час работы нельзя,
			// а писать после каждого ответа — лишние тысячи записей на диск.
			save := done%20 == 0
			mu.Unlock()

			if report != nil {
				report(snapshot)
			}
			if save {
				mu.Lock()
				err := g.saveCommunities(c)
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}

	for i := 0; i < opt.Workers; i++ {
		wg.Add(1)
		go worker()
	}
	for _, i := range work {
		if ctx.Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	if ctx.Err() != nil {
		// Обрыв не повод потерять сделанное: пишем и уходим.
		_ = g.saveCommunities(c)
		return ctx.Err()
	}
	return g.saveCommunities(c)
}

// askSummary задаёт модели один вопрос про одно сообщество.
func (g *Graph) askSummary(ctx context.Context, ex Extractor, com *Community,
	opt SummaryOpts) (summaryOut, error) {

	var b strings.Builder
	b.WriteString("Понятия группы (по убыванию частоты):\n")
	names := map[uint32]string{}
	for i, id := range com.Members {
		if i >= opt.MaxMembers {
			break
		}
		e, ok := g.Entities().Get(id)
		if !ok {
			continue
		}
		names[id] = e.Name
		fmt.Fprintf(&b, "- %s (%s, упоминаний %d)\n", e.Name, e.Type, e.Count)
	}

	b.WriteString("\nСвязи между ними:\n")
	shown := 0
	for _, id := range com.Members {
		if shown >= opt.MaxRelations {
			break
		}
		if _, ok := names[id]; !ok {
			continue
		}
		for _, ed := range g.Edges().Of(id) {
			dst, ok := names[ed.Dst]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "- %s → %s (%s)\n", names[id], dst, RelName(ed.Type))
			if shown++; shown >= opt.MaxRelations {
				break
			}
		}
	}

	raw, err := ex.Extract(ctx, SummaryPrompt, b.String())
	if err != nil {
		return summaryOut{}, err
	}
	var out struct {
		Title   string   `json:"title"`
		Summary string   `json:"summary"`
		Key     []string `json:"key"`
		Rating  int      `json:"rating"`
		Why     string   `json:"why"`
	}
	if err := json.Unmarshal([]byte(firstJSONObject(raw)), &out); err != nil {
		return summaryOut{}, fmt.Errorf("ответ не разобрался: %w", err)
	}
	if strings.TrimSpace(out.Title) == "" {
		return summaryOut{}, fmt.Errorf("пустой заголовок")
	}
	// Оценка вне диапазона — не беда: модель иногда пишет 0 или 12. Приводим
	// к границам, а не отбрасываем весь ответ из-за одного числа.
	if out.Rating < 0 {
		out.Rating = 0
	}
	if out.Rating > 10 {
		out.Rating = 10
	}
	return summaryOut{
		Title:   strings.TrimSpace(out.Title),
		Summary: strings.TrimSpace(out.Summary),
		Key:     out.Key,
		Rating:  out.Rating,
		Why:     strings.TrimSpace(out.Why),
	}, nil
}

// summaryOut — что модель сказала о сообществе.
type summaryOut struct {
	Title   string
	Summary string
	Key     []string
	Rating  int
	Why     string
}

// booksOfCommunity — книги, из которых пришли понятия сообщества, по убыванию
// числа упоминаний. Нужны обзору: тема без источников не проверяется.
func (g *Graph) booksOfCommunity(com *Community) []string {
	count := map[uint32]int{}
	for i, id := range com.Members {
		if i >= 60 { // хвост на состав книг уже не влияет
			break
		}
		for _, key := range g.Mentions().Of(id) {
			count[key.Doc]++
		}
	}
	docs := make([]uint32, 0, len(count))
	for d := range count {
		docs = append(docs, d)
	}
	sort.Slice(docs, func(i, j int) bool {
		if count[docs[i]] != count[docs[j]] {
			return count[docs[i]] > count[docs[j]]
		}
		return docs[i] < docs[j]
	})
	out := make([]string, 0, 5)
	for i, d := range docs {
		if i >= 5 {
			break
		}
		out = append(out, fmt.Sprintf("%d", d))
	}
	return out
}
