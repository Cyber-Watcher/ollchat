package census

// Перепись концов кусков (этап 112, Е4): какая доля кусков обрезана посреди
// предложения. Книга («Enterprise Guide for Implementing Generative AI», 2025,
// стр. 188–189) требует от нарезки потолка размера ПЛЮС логического окончания
// объекта. Наш `pack` собирает куски из целых абзацев и режет слишком длинный
// абзац по предложениям (`internal/kb/chunk.go`), но доли обрезанных никто не
// мерил. Карта не нужна: читается готовый индекс.
//
// Кусок «обрезан», если его последний знак (без закрывающих кавычек и скобок) —
// не конец предложения. Начало проверяется отдельно: строчная буква первой —
// кусок начат посреди предложения (так бывает и от перекрытия нарезки). Код,
// таблицы, оглавления и списки литературы не считаются: у них нет предложений.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// endsSentence — последний значимый знак текста закрывает предложение.
func endsSentence(t string) bool {
	t = strings.TrimRightFunc(t, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("»\"')]*_`”’", r)
	})
	r, _ := utf8.DecodeLastRuneInString(t)
	return r != utf8.RuneError && strings.ContainsRune(".!?…:;", r)
}

// tailLine — последняя строка текста кончается короткой отдельной строкой (≤ 80 знаков
// после перевода строки): заголовок или подпись, оторванные от своего раздела, а не обрыв
// фразы.
func tailLine(t string) bool {
	t = strings.TrimRightFunc(t, unicode.IsSpace)
	i := strings.LastIndexByte(t, '\n')
	return i >= 0 && utf8.RuneCountInString(strings.TrimSpace(t[i+1:])) <= 80
}

// startsMidSentence — первая буква текста строчная.
func startsMidSentence(t string) bool {
	for _, r := range strings.TrimSpace(t) {
		if unicode.IsLetter(r) {
			return unicode.IsLower(r)
		}
		if !unicode.IsPunct(r) && !unicode.IsSpace(r) {
			return false
		}
	}
	return false
}

func chunkEnds(stdout io.Writer, cfg *config.Config, collName string, examples int) error {
	base, c, err := openColl(cfg, collName)
	if err != nil {
		return err
	}
	defer base.Close()
	fmt.Fprintln(os.Stderr, "читаю куски коллекции…")
	type tally struct{ prose, cutEnd, cutStart, headTail int }
	byFmt := map[string]*tally{}
	var all tally
	skipped := 0
	var shown []string
	err = c.EachChunk(kb.ChunkFilter{}, func(ci kb.ChunkInfo) error {
		if ci.Code || ci.Table || ci.TOC || ci.Refs {
			skipped++
			return nil
		}
		ext := strings.ToLower(filepath.Ext(ci.Book.Path))
		t := byFmt[ext]
		if t == nil {
			t = &tally{}
			byFmt[ext] = t
		}
		notSentence, start := !endsSentence(ci.Text), startsMidSentence(ci.Text)
		head := notSentence && tailLine(ci.Text)
		end := notSentence && !head
		for _, x := range []*tally{t, &all} {
			x.prose++
			if end {
				x.cutEnd++
			}
			if head {
				x.headTail++
			}
			if start {
				x.cutStart++
			}
		}
		if (end || head) && all.prose%997 == 0 {
			tail := []rune(strings.TrimSpace(ci.Text))
			if len(tail) > 70 {
				tail = tail[len(tail)-70:]
			}
			kind := "оборван"
			if head {
				kind = "короткая строка (" + ext + ")"
			}
			if len(shown) < 2*examples {
				shown = append(shown, kind+": …"+strings.ReplaceAll(string(tail), "\n", " ⏎ "))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	share := func(a, b int) float64 { return float64(a) / float64(max(b, 1)) }
	fmt.Fprintf(stdout, "коллекция %s: кусков прозы %d (код, таблицы, оглавления, литература не в счёт: %d)\n",
		collName, all.prose, skipped)
	fmt.Fprintf(stdout, "оборван посреди фразы: %d (%.3f); кончается короткой строкой (заголовок, подпись): %d (%.3f); "+
		"начинается со строчной буквы: %d (%.3f)\n",
		all.cutEnd, share(all.cutEnd, all.prose), all.headTail, share(all.headTail, all.prose),
		all.cutStart, share(all.cutStart, all.prose))
	exts := make([]string, 0, len(byFmt))
	for e := range byFmt {
		exts = append(exts, e)
	}
	sort.Slice(exts, func(i, j int) bool { return byFmt[exts[i]].prose > byFmt[exts[j]].prose })
	fmt.Fprintf(stdout, "\n%-8s %9s %16s %18s %16s\n", "формат", "кусков", "оборван", "короткая строка", "начало строчное")
	for _, e := range exts {
		t := byFmt[e]
		fmt.Fprintf(stdout, "%-8s %9d %9d %.3f %11d %.3f %9d %.3f\n", e, t.prose, t.cutEnd, share(t.cutEnd, t.prose),
			t.headTail, share(t.headTail, t.prose), t.cutStart, share(t.cutStart, t.prose))
	}
	if len(shown) > 0 {
		fmt.Fprintf(stdout, "\nпримеры обрезанных концов:\n")
		for _, s := range shown {
			fmt.Fprintf(stdout, "  %s\n", s)
		}
	}
	return nil
}
