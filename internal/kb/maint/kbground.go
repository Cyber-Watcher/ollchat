package maint

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"unicode"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
)

// Ground — проверка заземлённости ответов реранкером вместо судьи-модели
// (этап 112, О11; «60%–70% of hallucinations at less than 5% of the compute
// cost», «Agentic GraphRAG», Alcaraz, 2026, стр. 380 — цифра книги, не наша).
//
// Каждый ответ режется на предложения; каждое предложение оценивается
// реранкером против кусков, поданных модели, и берётся лучшая оценка.
// У ответа два числа: средняя лучшая оценка предложений и доля предложений
// с лучшей оценкой не ниже нуля. Вопрос замера — ОТДЕЛЯЮТ ли эти числа верные
// ответы (поле ok из kbanswers.py: факт из эталона прозвучал) от неверных;
// мера отделения — AUC: вероятность, что у случайного верного ответа число
// выше, чем у случайного неверного (0,5 — не отделяет вовсе).
//
// Источники — файлы kbanswers.py: answers (руки search, search-full, gold,
// gold-full) и refs (`--ask --mix-only`: какие куски поданы модели). Для рук
// с эталоном контекст собирается ПРИБЛИЖЁННО: эталон первым, затем подмес
// поиска, всего не больше восьми. Сверка идёт по полным текстам кусков, хотя
// руки без «-full» видели выдержки по 320 знаков.
//
// out — строки по ответам (JSON); пусто — не писать.
func Ground(stdout io.Writer, cfg *config.Config, name, answersPath, refsPath, out string) error {
	answers, err := readGroundAnswers(answersPath)
	if err != nil {
		return err
	}
	refs, err := readGroundRefs(refsPath)
	if err != nil {
		return err
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rr := kbrerank.New(cfg.KB.RerankOptions())
	if rr == nil {
		return fmt.Errorf("переранжирование не настроено: задайте kb.rerank_url в %s", cfg.Path)
	}
	if err := rr.Check(ctx); err != nil {
		return err
	}

	var rows *os.File
	if out != "" {
		if rows, err = os.Create(out); err != nil {
			return fmt.Errorf("строки замера: %w", err)
		}
		defer rows.Close()
	}
	enc := json.NewEncoder(io.Discard)
	if rows != nil {
		enc = json.NewEncoder(rows)
		enc.SetEscapeHTML(false)
	}

	fmt.Fprintf(os.Stderr, "сверяю %d ответов с поданными кусками (реранкер %s)…\n", len(answers), rr.Model())
	byArm := map[string][]groundRow{}
	var arms []string
	for i, a := range answers {
		ctxRefs := refs[a.Query]
		if strings.HasPrefix(a.Arm, "gold") {
			ctxRefs = withGold(a.ChunkID, ctxRefs, 8)
		}
		texts := chunkTexts(coll, ctxRefs)
		sents := answerSentences(a.Answer, 40)
		if len(texts) == 0 || len(sents) == 0 {
			continue
		}
		row := groundRow{Arm: a.Arm, Query: a.Query, OK: a.OK, Sentences: len(sents), Chunks: len(texts)}
		var sum float64
		for _, s := range sents {
			scores, err := rr.Rerank(ctx, s, texts)
			if err != nil {
				return fmt.Errorf("реранкер, ответ %d: %w", i+1, err)
			}
			if len(scores) != len(texts) {
				return fmt.Errorf("реранкер, ответ %d: оценок %d на %d кусков", i+1, len(scores), len(texts))
			}
			best := scores[0]
			for _, v := range scores[1:] {
				if v > best {
					best = v
				}
			}
			sum += best
			if best >= 0 {
				row.Share++
			}
		}
		row.Mean = sum / float64(len(sents))
		row.Share /= float64(len(sents))
		if _, ok := byArm[a.Arm]; !ok {
			arms = append(arms, a.Arm)
		}
		byArm[a.Arm] = append(byArm[a.Arm], row)
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("строки замера %s: %w", out, err)
		}
		if (i+1)%20 == 0 {
			fmt.Fprintf(os.Stderr, "  %d из %d\n", i+1, len(answers))
		}
	}

	fmt.Fprintf(stdout, "проверка ответов реранкером (%s): лучшая оценка предложения ответа против поданных кусков\n\n", rr.Model())
	fmt.Fprintf(stdout, "%-12s %7s %7s %12s %12s %10s %10s\n",
		"рука", "ответов", "верных", "ср. верных", "ср. невер.", "AUC сред.", "AUC доли")
	fmt.Fprintln(stdout, "  "+dashes(74))
	for _, arm := range arms {
		rs := byArm[arm]
		var okMean, badMean []float64
		var okShare, badShare []float64
		for _, r := range rs {
			if r.OK {
				okMean, okShare = append(okMean, r.Mean), append(okShare, r.Share)
			} else {
				badMean, badShare = append(badMean, r.Mean), append(badShare, r.Share)
			}
		}
		fmt.Fprintf(stdout, "%-12s %7d %7d %12.2f %12.2f %10.2f %10.2f\n", arm, len(rs), len(okMean),
			avg(okMean), avg(badMean), aucOf(okMean, badMean), aucOf(okShare, badShare))
	}
	fmt.Fprintln(stdout, "\nAUC — вероятность, что у верного ответа число выше, чем у неверного; 0,5 — не отделяет.")
	fmt.Fprintln(stdout, "У рук gold и gold-full контекст приближённый: эталон и подмес поиска.")
	return nil
}

// groundRow — один ответ в замере О11.
type groundRow struct {
	Arm       string  `json:"arm"`
	Query     string  `json:"query"`
	OK        bool    `json:"ok"`
	Sentences int     `json:"sentences"`
	Chunks    int     `json:"chunks"`
	Mean      float64 `json:"mean"`  // средняя лучшая оценка предложения
	Share     float64 `json:"share"` // доля предложений с лучшей оценкой ≥ 0
}

// groundAnswer — строка answers.jsonl из kbanswers.py (нужные поля).
type groundAnswer struct {
	Arm     string `json:"arm"`
	Query   string `json:"query"`
	ChunkID string `json:"chunk_id"`
	Answer  string `json:"answer"`
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
}

// readGroundAnswers читает ответы; с ошибкой и пустые пропускаются.
func readGroundAnswers(path string) ([]groundAnswer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ответы %s: %w", path, err)
	}
	defer f.Close()
	var out []groundAnswer
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var a groundAnswer
		if json.Unmarshal(sc.Bytes(), &a) != nil || a.Error != "" || strings.TrimSpace(a.Answer) == "" {
			continue
		}
		out = append(out, a)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ответы %s: %w", path, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("в %s нет ни одного ответа", path)
	}
	return out, nil
}

// readGroundRefs читает подмесы: вопрос → ссылки на поданные куски.
func readGroundRefs(path string) (map[string][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("подмесы %s: %w", path, err)
	}
	defer f.Close()
	out := map[string][]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var r struct {
			Query   string   `json:"query"`
			MixRefs []string `json:"mix_refs"`
		}
		if json.Unmarshal(sc.Bytes(), &r) == nil && len(r.MixRefs) > 0 {
			out[r.Query] = r.MixRefs
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("подмесы %s: %w", path, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("в %s нет ни одного подмеса (поле mix_refs)", path)
	}
	return out, nil
}

// withGold ставит эталон первым, затем подмес без повтора эталона, не
// больше limit ссылок.
func withGold(gold string, refs []string, limit int) []string {
	out := []string{}
	if gold != "" {
		out = append(out, gold)
	}
	for _, r := range refs {
		if len(out) == limit {
			break
		}
		if r != gold {
			out = append(out, r)
		}
	}
	return out
}

// chunkTexts — тексты кусков по ссылкам; ненайденные пропускаются.
func chunkTexts(coll *kb.Collection, refs []string) []string {
	var out []string
	for _, id := range refs {
		doc, ok := kb.DocOfChunkID(id)
		if !ok {
			continue
		}
		i := strings.LastIndexByte(id, '#')
		var ord uint32
		if _, err := fmt.Sscanf(id[i+1:], "%d", &ord); err != nil {
			continue
		}
		if c, ok := coll.ChunkByRef(doc, ord); ok && c.Text != "" {
			out = append(out, c.Text)
		}
	}
	return out
}

// answerSentences режет ответ на предложения: по строкам и по концу фразы.
// Ограды кода и куски короче 15 букв (заголовки, маркеры таблиц) пропускаются;
// больше limit предложений не берётся.
func answerSentences(answer string, limit int) []string {
	var out []string
	for _, line := range strings.Split(answer, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") {
			continue
		}
		start := 0
		runes := []rune(line)
		for i, r := range runes {
			end := i == len(runes)-1
			if (r == '.' || r == '!' || r == '?') && (end || unicode.IsSpace(runes[i+1])) || end {
				s := strings.TrimSpace(string(runes[start : i+1]))
				start = i + 1
				if letterCount(s) >= 15 {
					out = append(out, s)
					if len(out) == limit {
						return out
					}
				}
			}
		}
	}
	return out
}

func letterCount(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

func avg(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// aucOf — вероятность, что случайное число из pos больше случайного из neg
// (равенство — половина). Пустая сторона — 0,5: отделять нечего.
func aucOf(pos, neg []float64) float64 {
	if len(pos) == 0 || len(neg) == 0 {
		return 0.5
	}
	sorted := append([]float64(nil), neg...)
	sort.Float64s(sorted)
	var wins float64
	for _, p := range pos {
		lo := sort.SearchFloat64s(sorted, p)
		hi := sort.Search(len(sorted), func(i int) bool { return sorted[i] > p })
		wins += float64(lo) + 0.5*float64(hi-lo)
	}
	return wins / float64(len(pos)*len(neg))
}
