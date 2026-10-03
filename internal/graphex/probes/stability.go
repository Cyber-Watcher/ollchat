// stability.go — режим `-only stability`: устойчивость извлечения. Объединяет
// бывшие `tempprobe` (`-axis temp`) и `textprobe` (`-axis textfix`) — этап 104,
// Ж5.5/Д4 книг и Ж5.2, перенос в один ключ — этап 114, Г3.
//
// **Общая для обеих осей схема.** N кусков прозы равномерно по библиотеке,
// по три-четыре прогона на кусок, Жаккар между прогонами как мера
// устойчивости. Разное — что меняется между прогонами: температура запроса
// (ось temp) или починка текста куска перед отправкой (ось textfix). Ничего
// не пишет ни в граф, ни в коллекцию; карта нужна.
package probes

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/graphex"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// runStability — разбор оси и вызов нужной ветки.
func runStability(stdout io.Writer, cfg *config.Config, collName, axis string, n int, out string, cold, warm float64, minWraps int, timeout time.Duration) error {
	switch axis {
	case "temp":
		return stabilityTemp(stdout, cfg, collName, n, out, cold, warm, timeout)
	case "textfix":
		return stabilityTextfix(stdout, cfg, collName, n, out, minWraps, timeout)
	case "":
		return fmt.Errorf("режиму stability нужна ось: -axis temp|textfix")
	}
	return fmt.Errorf("неизвестная ось %q; есть: temp, textfix", axis)
}

// jaccard — сходство двух наборов написаний (мера устойчивости между
// прогонами). Общая для обеих осей.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// thinChunks — равномерная выборка n кусков из cand по всему обходу: то же
// прореживание шагом, что и в переписях census. Общая для обеих осей.
func thinChunks[T any](cand []T, n int) []T {
	// n<=0 — без прореживания: при нуле шаг ниже делил на ноль (паника мимо
	// stop, падал весь ollchat).
	if n <= 0 || len(cand) <= n {
		return cand
	}
	step := len(cand) / n
	var thin []T
	for i := 0; i < len(cand) && len(thin) < n; i += step {
		thin = append(thin, cand[i])
	}
	return thin
}

// newIn — сколько ключей a нет в b.
func newIn(a, b map[string]bool) int {
	c := 0
	for k := range a {
		if !b[k] {
			c++
		}
	}
	return c
}

// ---- ось temp (бывший tempprobe) --------------------------------------

type tempTally struct {
	entities, relations, failed, seen int
	pairs                             int
	jac, gain, gainRel                float64 // сходство двух прогонов; прибавка второго прохода (понятий, связей) к первому
}

// stabilityTemp — температура извлечения 0 против рабочей и польза второго
// прохода по тому же куску.
func stabilityTemp(stdout io.Writer, cfg *config.Config, collName string, n int, out string, cold, warm float64, timeout time.Duration) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	if warm < 0 {
		warm = cfg.Graph.Temperature
	}

	// Отбор: проза (не код, не служебные), равномерно по всему обходу.
	var cand []kb.ChunkInfo
	_ = c.EachChunk(kb.ChunkFilter{}, func(ci kb.ChunkInfo) error {
		if ci.Code || ci.TOC || ci.Refs || len(ci.Text) < 600 {
			return nil
		}
		cand = append(cand, ci)
		return nil
	})
	cand = thinChunks(cand, n)
	books := map[string]bool{}
	for _, p := range cand {
		books[p.Book.Path] = true
	}
	fmt.Fprintf(stdout, "коллекция %s; к замеру: %d кусков из %d книг; температуры %.2f и %.2f, по два прогона\n", collName, len(cand), len(books), cold, warm)

	mk := func(t float64) (*graphex.Extractor, error) {
		o := cfg.Graph.ExtractOptions()
		o.Temperature = t
		ex := graphex.New(o, cfg.Servers[0].URL, timeout, nil)
		if ex == nil {
			return nil, fmt.Errorf("извлечение не настроено: задайте graph.model")
		}
		return ex, nil
	}
	exCold, err := mk(cold)
	if err != nil {
		return err
	}
	exWarm, err := mk(warm)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "модель %s\n\n", exCold.Model())

	var tsv *os.File
	if out != "" {
		if tsv, err = os.Create(out); err != nil {
			return err
		}
		defer tsv.Close()
		fmt.Fprintln(tsv, "кусок\tкнига\tтемпература\tпрогон\tпонятий\tсвязей\tвидимых\tимена")
	}

	ctx := context.Background()
	var tCold, tWarm tempTally
	started := time.Now()
	for i, p := range cand {
		title := p.Book.Title
		if title == "" {
			title = filepath.Base(p.Book.Path)
		}
		user := graph.UserPrompt(title, p.Unit, p.UnitFrom, p.UnitTo, p.Text)
		joined, hyph := graph.MatchText(p.Text)

		run := func(ex *graphex.Extractor, temp float64, pass int, t *tempTally) (map[string]bool, map[string]bool) {
			answer, err := ex.Extract(ctx, graph.SystemPrompt, user)
			if err != nil {
				t.failed++
				return nil, nil
			}
			facts, err := graph.ParseFacts(answer, p.Text)
			if err != nil {
				t.failed++
				return nil, nil
			}
			names, rels := map[string]bool{}, map[string]bool{}
			seen := 0
			for _, e := range facts.Entities {
				names[graph.MatchName(e.Name)] = true
				if graph.SeenInText(joined, hyph, e.Name) {
					seen++
				}
			}
			for _, r := range facts.Relations {
				a, b := graph.MatchName(r.Src), graph.MatchName(r.Dst)
				if a > b {
					a, b = b, a
				}
				rels[a+"|"+b] = true
			}
			t.entities += len(facts.Entities)
			t.relations += len(facts.Relations)
			t.seen += seen
			if tsv != nil {
				var list []string
				for k := range names {
					list = append(list, k)
				}
				fmt.Fprintf(tsv, "%d#%d\t%s\t%.2f\t%d\t%d\t%d\t%d\t%s\n", p.Doc, p.Ord,
					filepath.Base(p.Book.Path), temp, pass, len(facts.Entities), len(facts.Relations), seen, strings.Join(list, " | "))
			}
			return names, rels
		}
		measure := func(ex *graphex.Extractor, temp float64, t *tempTally) {
			n1, r1 := run(ex, temp, 1, t)
			n2, r2 := run(ex, temp, 2, t)
			if n1 == nil || n2 == nil {
				return
			}
			t.pairs++
			t.jac += jaccard(n1, n2)
			t.gain += float64(newIn(n2, n1))
			t.gainRel += float64(newIn(r2, r1))
		}
		measure(exCold, cold, &tCold)
		measure(exWarm, warm, &tWarm)
		if (i+1)%10 == 0 {
			fmt.Fprintf(os.Stderr, "%s %d/%d кусков, прошло %s\n", time.Now().Format("15:04:05"), i+1, len(cand),
				time.Since(started).Round(time.Second))
		}
	}

	fmt.Fprintf(stdout, "%-12s %9s %9s %6s %13s %10s %14s %14s\n", "температура", "понятий", "связей", "сбоев", "видны в куске", "Жаккар", "+понятий/2-й", "+связей/2-й")
	show := func(label string, t tempTally) {
		pct := 0.0
		if t.entities > 0 {
			pct = 100 * float64(t.seen) / float64(t.entities)
		}
		per := func(x float64) float64 {
			if t.pairs == 0 {
				return 0
			}
			return x / float64(t.pairs)
		}
		fmt.Fprintf(stdout, "%-12s %9d %9d %6d %12.1f%% %10.3f %14.2f %14.2f\n", label, t.entities, t.relations, t.failed, pct,
			per(t.jac), per(t.gain), per(t.gainRel))
	}
	show(fmt.Sprintf("%.2f", cold), tCold)
	show(fmt.Sprintf("%.2f", warm), tWarm)
	fmt.Fprintln(stdout, "\n«понятий» и «связей» — за оба прогона вместе; «+понятий/2-й» — сколько имён на кусок второй прогон добавил к первому (польза второго прохода)")
	fmt.Fprintf(stdout, "замер занял %s\n", time.Since(started).Round(time.Second))
	return nil
}

// ---- ось textfix (бывший textprobe) ------------------------------------

func isHyph(r rune) bool {
	return r == '-' || r == '\u00ad' || r == '\u2010' || r == '\u2011'
}

// wrapAfter — место продолжения слова после знака переноса i, либо 0.
func wrapAfter(r []rune, i int) int {
	if i == 0 || !unicode.IsLetter(r[i-1]) {
		return 0
	}
	nl := 0
	for j := i + 1; j < len(r) && j < i+120; j++ {
		switch c := r[j]; {
		case c == '\n':
			nl++
			if nl > 1 {
				return 0
			}
		case c == ' ' || c == '\t' || c == '\r':
		case unicode.IsLetter(c):
			if nl == 1 {
				return j
			}
			return 0
		default:
			return 0
		}
	}
	return 0
}

// repairText чинит запись текста, не трогая его смысл: перенос слова
// склеивается (строчное продолжение — перенос; заглавное — составное слово,
// дефис остаётся), лигатуры раскрываются, невидимые знаки и мягкие переносы
// уходят.
func repairText(text string) (string, int) {
	r := []rune(text)
	var b strings.Builder
	fixed := 0
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case isHyph(c):
			if next := wrapAfter(r, i); next > 0 {
				if c != '\u00ad' && !unicode.IsLower(r[next]) {
					b.WriteByte('-')
				}
				fixed++
				i = next - 1
				continue
			}
			if c == '\u00ad' {
				// Посреди строки в PDF так записан обычный дефис.
				if i > 0 && i+1 < len(r) && unicode.IsLetter(r[i-1]) && unicode.IsLetter(r[i+1]) {
					b.WriteByte('-')
				}
				continue
			}
			if c != '-' {
				b.WriteByte('-')
				continue
			}
			b.WriteRune(c)
		case (c >= '\u200b' && c <= '\u200d') || c == '\u2060' || c == '\ufeff':
		case c == '\ufb00':
			b.WriteString("ff")
		case c == '\ufb01':
			b.WriteString("fi")
		case c == '\ufb02':
			b.WriteString("fl")
		case c == '\ufb03':
			b.WriteString("ffi")
		case c == '\ufb04':
			b.WriteString("ffl")
		default:
			b.WriteRune(c)
		}
	}
	return b.String(), fixed
}

type textfixTally struct {
	entities, relations, orphans, failed int
	seen, broken                         int // имён, видимых в куске; имён с обрывком переноса
}

// stabilityTextfix — меняет ли починенный текст куска то, что извлекает
// модель.
func stabilityTextfix(stdout io.Writer, cfg *config.Config, collName string, n int, out string, minWraps int, timeout time.Duration) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()

	// Отбор: куски с переносами, равномерно по всему обходу.
	type pick struct {
		info  kb.ChunkInfo
		fixed string
		wraps int
	}
	var cand []pick
	_ = c.EachChunk(kb.ChunkFilter{}, func(ci kb.ChunkInfo) error {
		if ci.Code || ci.TOC || ci.Refs {
			return nil
		}
		fixed, k := repairText(ci.Text)
		if k >= minWraps {
			cand = append(cand, pick{ci, fixed, k})
		}
		return nil
	})
	fmt.Fprintf(stdout, "кусков с %d+ переносами: %d\n", minWraps, len(cand))
	cand = thinChunks(cand, n)
	books := map[string]bool{}
	for _, p := range cand {
		books[p.info.Book.Path] = true
	}
	fmt.Fprintf(stdout, "к замеру: %d кусков из %d книг\n", len(cand), len(books))

	ex := graphex.New(cfg.Graph.ExtractOptions(), cfg.Servers[0].URL, timeout, nil)
	if ex == nil {
		return fmt.Errorf("извлечение не настроено: задайте graph.model")
	}
	fmt.Fprintf(stdout, "модель %s, температура из настроек\n\n", ex.Model())

	var tsv *os.File
	if out != "" {
		if tsv, err = os.Create(out); err != nil {
			return err
		}
		defer tsv.Close()
		fmt.Fprintln(tsv, "кусок\tкнига\tпереносов\tусловие\tпонятий\tсвязей\tвидимых\tс_обрывком\tимена")
	}

	ctx := context.Background()
	var raw1, raw2, fix textfixTally
	var jRawRaw, jRawFix float64
	pairs := 0
	started := time.Now()
	for i, p := range cand {
		title := p.info.Book.Title
		if title == "" {
			title = filepath.Base(p.info.Book.Path)
		}
		userRaw := graph.UserPrompt(title, p.info.Unit, p.info.UnitFrom, p.info.UnitTo, p.info.Text)
		userFix := graph.UserPrompt(title, p.info.Unit, p.info.UnitFrom, p.info.UnitTo, p.fixed)
		joined, hyph := graph.MatchText(p.info.Text)

		run := func(label, user, chunk string, t *textfixTally) map[string]bool {
			answer, err := ex.Extract(ctx, graph.SystemPrompt, user)
			if err != nil {
				t.failed++
				return nil
			}
			facts, err := graph.ParseFacts(answer, chunk)
			if err != nil {
				t.failed++
				return nil
			}
			linked := map[string]bool{}
			for _, r := range facts.Relations {
				linked[strings.ToLower(r.Src)] = true
				linked[strings.ToLower(r.Dst)] = true
			}
			names := map[string]bool{}
			seen, broken := 0, 0
			for _, e := range facts.Entities {
				low := strings.ToLower(e.Name)
				names[graph.MatchName(e.Name)] = true
				if !linked[low] {
					t.orphans++
				}
				if graph.SeenInText(joined, hyph, e.Name) {
					seen++
				}
				// Обрывок переноса в имени: «искусст- венные», «Func\u2010 tionality».
				if strings.Contains(e.Name, "- ") || strings.Contains(e.Name, "\u2010 ") || strings.ContainsRune(e.Name, '\u00ad') {
					broken++
				}
			}
			t.entities += len(facts.Entities)
			t.relations += len(facts.Relations)
			t.seen += seen
			t.broken += broken
			if tsv != nil {
				var list []string
				for k := range names {
					list = append(list, k)
				}
				fmt.Fprintf(tsv, "%d#%d\t%s\t%d\t%s\t%d\t%d\t%d\t%d\t%s\n", p.info.Doc, p.info.Ord,
					filepath.Base(p.info.Book.Path), p.wraps, label, len(facts.Entities), len(facts.Relations),
					seen, broken, strings.Join(list, " | "))
			}
			return names
		}
		a := run("сырой-1", userRaw, p.info.Text, &raw1)
		b := run("сырой-2", userRaw, p.info.Text, &raw2)
		f := run("починенный", userFix, p.fixed, &fix)
		if a != nil && b != nil && f != nil {
			jRawRaw += jaccard(a, b)
			jRawFix += (jaccard(a, f) + jaccard(b, f)) / 2
			pairs++
		}
		if (i+1)%10 == 0 {
			fmt.Fprintf(os.Stderr, "%s %d/%d кусков, прошло %s\n", time.Now().Format("15:04:05"), i+1, len(cand),
				time.Since(started).Round(time.Second))
		}
	}

	fmt.Fprintf(stdout, "%-14s %8s %8s %11s %7s %12s %11s\n", "условие", "понятий", "связей", "без связей", "сбоев", "видны в куске", "с обрывком")
	show := func(label string, t textfixTally) {
		pct := 0.0
		if t.entities > 0 {
			pct = 100 * float64(t.seen) / float64(t.entities)
		}
		fmt.Fprintf(stdout, "%-14s %8d %8d %11d %7d %11.1f%% %11d\n", label, t.entities, t.relations, t.orphans, t.failed, pct, t.broken)
	}
	show("сырой-1", raw1)
	show("сырой-2", raw2)
	show("починенный", fix)
	if pairs > 0 {
		fmt.Fprintf(stdout, "\nсходство состава понятий (Жаккар, среднее по %d кускам):\n", pairs)
		fmt.Fprintf(stdout, "  сырой против сырого (шумовой пол):  %.3f\n", jRawRaw/float64(pairs))
		fmt.Fprintf(stdout, "  сырой против починенного:           %.3f\n", jRawFix/float64(pairs))
	}
	fmt.Fprintf(stdout, "замер занял %s\n", time.Since(started).Round(time.Second))
	return nil
}
