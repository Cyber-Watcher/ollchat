package graph

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Выводы по теме — пятый раздел отчёта о сообществе.
//
// Резюме отвечает на вопрос «о чём эта тема». Выводы отвечают на другой вопрос —
// «что именно про неё известно»: список содержательных утверждений, каждое
// с пояснением. В MS GraphRAG это DETAILED FINDINGS: «a list of 5-10 key
// insights about the community. Each insight should have a short summary
// followed by multiple paragraphs of explanatory text» (Essential GraphRAG,
// 2025, стр. 121–122).
//
// **Почему не для всех тем.** Замер 26.08.2026 по 336 разобранным темам: резюме
// стоит 216 токенов выдачи, разбор — 581, то есть **в 2.7 раза дороже**;
// прогон 337 тем занял 22 минуты, все 2 590 тем стоили бы около полутора часов.
//
// Заранее я оценивал разницу в 6–15 раз и ошибся: книга просит выводы «с
// несколькими абзацами пояснения» на каждый, а промпт ниже — по 2–4 предложения.
// Оценка давалась для книжного варианта, а сделан облегчённый, и пересчитать её
// я забыл. Числа выше — замеренные.
//
// Отбор по важности всё равно оправдан, но не ценой, а пользой: у темы с низкой
// оценкой выводам взяться неоткуда, и разбор мусорного набора понятий — это
// правдоподобный текст ни о чём.
//
// **Почему не в обзоре.** Обзор из пяти тем укладывается в 1.05 тыс. токенов,
// с выводами занял бы 3.6 тыс. — втрое больше (замер, не оценка). Это заметная
// доля окна в 32k, которое нужно самим книгам и ответу, поэтому выводы отдаются
// отдельным вызовом (инструмент graph_topic), а обзор остаётся картой тем.

// Finding — один вывод по теме.
type Finding struct {
	Title string `json:"title"` // о чём вывод, одной строкой
	Text  string `json:"text"`  // пояснение
}

// FindingsPrompt — системное сообщение для выводов по теме.
//
// Требование «не выдумывать» здесь строже, чем в резюме: резюме пересказывает
// список понятий, а вывод претендует на утверждение о предмете, и выдуманное
// утверждение выглядит убедительнее выдуманного названия.
//
//go:embed prompts/findings.txt
var FindingsPrompt string

// FindingsOpts — как считать выводы.
type FindingsOpts struct {
	// MinRating — брать только темы с оценкой не ниже. 0 — восемь.
	// Оценку ставит модель при написании резюме; по ней же обзор отбирает темы.
	MinRating int

	// MinMembers — брать только темы не меньше стольких понятий. 0 — двадцать.
	// У темы из пяти понятий выводам взяться неоткуда.
	MinMembers int

	// MaxMembers и MaxRelations — сколько показывать модели. 0 — сорок и тридцать.
	MaxMembers   int
	MaxRelations int

	// Workers — сколько тем считать одновременно. 0 — четыре.
	Workers int

	// Redo — пересчитать даже те темы, у которых выводы уже есть.
	Redo bool
}

func (o FindingsOpts) norm() FindingsOpts {
	if o.MinRating <= 0 {
		o.MinRating = 8
	}
	if o.MinMembers <= 0 {
		o.MinMembers = 20
	}
	if o.MaxMembers <= 0 {
		o.MaxMembers = 40
	}
	if o.MaxRelations <= 0 {
		o.MaxRelations = 30
	}
	if o.Workers <= 0 {
		o.Workers = 4
	}
	return o
}

// FindingsProgress — ход работы.
type FindingsProgress struct {
	Done, Failed, Total int
	Title               string
	Elapsed             time.Duration
}

// SelectForFindings возвращает номера тем, которым положены выводы.
//
// Отдельно от самой работы, чтобы можно было спросить «сколько это будет»,
// не занимая карту.
func (c *Communities) SelectForFindings(o FindingsOpts) []int {
	o = o.norm()
	var out []int
	for i, com := range c.List {
		if com.Level != 0 || com.Title == "" {
			continue // без резюме нет ни оценки, ни описания темы
		}
		if com.Rating < o.MinRating || len(com.Members) < o.MinMembers {
			continue
		}
		if len(com.Findings) > 0 && !o.Redo {
			continue
		}
		out = append(out, i)
	}
	return out
}

// Findings пишет выводы по темам, прошедшим отбор.
//
// Устроено как Summarize и по тем же причинам: работа идёт в несколько потоков
// (между ответом и следующим запросом карта простаивает), запись в общий срез
// только под замком, сохранение на диск каждые двадцать тем — прогон на часы
// не должен терять всё при обрыве.
func (g *Graph) Findings(ctx context.Context, ex Extractor, c *Communities,
	opt FindingsOpts, report func(FindingsProgress)) error {

	opt = opt.norm()
	work := c.SelectForFindings(opt)
	started := time.Now()
	pr := FindingsProgress{Total: len(work)}
	if len(work) == 0 {
		return nil
	}

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

			found, err := g.askFindings(ctx, ex, &com, opt)

			mu.Lock()
			done++
			if err != nil {
				pr.Failed++
			} else {
				c.List[i].Findings = found
				pr.Done++
				pr.Title = com.Title
			}
			pr.Elapsed = time.Since(started)
			snapshot := pr
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
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()

	if report != nil {
		mu.Lock()
		snapshot := pr
		mu.Unlock()
		report(snapshot)
	}
	return g.saveCommunities(c)
}

// askFindings просит модель разобрать одну тему.
func (g *Graph) askFindings(ctx context.Context, ex Extractor, com *Community,
	opt FindingsOpts) ([]Finding, error) {

	var b strings.Builder
	fmt.Fprintf(&b, "Тема: %s\n", com.Title)
	if com.Summary != "" {
		fmt.Fprintf(&b, "Описание: %s\n", com.Summary)
	}
	b.WriteString("\nПонятия группы (по убыванию частоты):\n")
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

	raw, err := ex.Extract(ctx, FindingsPrompt, b.String())
	if err != nil {
		return nil, err
	}
	return parseFindings(raw)
}

// parseFindings разбирает ответ модели.
//
// Терпим к обрамлению: модель то отдаёт голый JSON, то заворачивает его
// в ограду ```json. Замер 26.08.2026 на составлении замерных вопросов: тот же
// glm на одном и том же промпте отвечал обоими способами вперемешку, и разбор,
// требовавший строгого JSON, отбраковал все семьдесят ответов подряд.
func parseFindings(raw string) ([]Finding, error) {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if i := strings.LastIndex(s, "}"); i >= 0 && i < len(s)-1 {
		s = s[:i+1]
	}
	var out struct {
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("ответ не разобран: %w", err)
	}
	res := make([]Finding, 0, len(out.Findings))
	for _, f := range out.Findings {
		f.Title = strings.TrimSpace(f.Title)
		f.Text = strings.TrimSpace(f.Text)
		if f.Title == "" || f.Text == "" {
			continue // половина вывода — не вывод
		}
		res = append(res, f)
		if len(res) >= 10 {
			break // предел из книги: больше десяти выводов не бывает полезно
		}
	}
	return res, nil
}
