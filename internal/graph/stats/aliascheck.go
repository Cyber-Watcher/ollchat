package stats

// Что изменит перевод сверки синонимов при сборке (`clean`, extract.go)
// на общую `graph.SeenInText` (этап 104, А3.4). Стенд: ничего не меняет.
//
// **Почему не «сколько отказов перевернётся» напрямую.** Отброшенные синонимы
// граф не хранит, а сырые ответы модели не сохраняются — прямой замер стоит
// прогона модели по кускам, то есть карты. Без карты меряется то, от чего
// исход зависит на деле: где две сверки РАСХОДЯТСЯ на настоящих текстах.
// Пары для сверки — (синоним понятия, кусок, где понятие упомянуто): это
// те самые строки, которые модель предлагает синонимами, в тех самых кусках.
//
// Четыре клетки: обе сверки «да», обе «нет», и две клетки расхождения —
// с примерами, потому что решать по ним надо глазами.

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// oldAliasSeen — сверка, как она стоит в `clean` сегодня: фраза целиком
// в нижнем регистре со схлопнутыми пробелами, по границам слова.
func oldAliasSeen(text, alias string) bool {
	h := strings.ToLower(strings.Join(strings.Fields(text), " "))
	a := strings.ToLower(strings.Join(strings.Fields(alias), " "))
	if a == "" {
		return false
	}
	// Ровно как isWordRune в extract.go: буква или цифра; «_» — граница слова.
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	for from := 0; ; {
		i := strings.Index(h[from:], a)
		if i < 0 {
			return false
		}
		i += from
		before, after := true, true
		if i > 0 {
			r, _ := utf8.DecodeLastRuneInString(h[:i])
			before = !word(r)
		}
		if end := i + len(a); end < len(h) {
			r, _ := utf8.DecodeRuneInString(h[end:])
			after = !word(r)
		}
		if before && after {
			return true
		}
		from = i + 1
	}
}

func aliasCheck(g *graph.Graph, c *kb.Collection, sample int, seed int64) {
	picked := sampleMentions(g, sample, seed)
	var pairs, bothYes, bothNo, onlyOld, onlyNew, onlyOldShort, newByUnderscore, newByHyphen int
	exOld := make([]string, 0, 12)
	exNew := make([]string, 0, 12)

	for _, m := range picked {
		e, ok := g.Entities().Get(m.ent)
		if !ok {
			continue
		}
		ci, ok := c.ChunkByRef(m.key.Doc, m.key.Ord)
		if !ok {
			continue
		}
		joined, hyph := graph.MatchText(ci.Text)
		for _, al := range g.Entities().DisplayAliases(e) {
			pairs++
			was, now := oldAliasSeen(ci.Text, al), graph.SeenInText(joined, hyph, al)
			switch {
			case was && now:
				bothYes++
			case !was && !now:
				bothNo++
			case was:
				onlyOld++
				if utf8.RuneCountInString(strings.TrimSpace(al)) < 3 {
					onlyOldShort++
				} else if len(exOld) < cap(exOld) {
					exOld = append(exOld, fmt.Sprintf("%-28s [%s] ← %s", cut(al, 28), cut(e.Name, 20), cut(oneLine(ci.Text), 70)))
				}
			default:
				onlyNew++
				// Отчего новая увидела: подчёркивание или дефис прочитаны пробелом
				// (часть идентификатора) — или починен сам текст (перенос, мягкий
				// перенос, лигатура, разрыв строки).
				switch {
				case oldAliasSeen(strings.NewReplacer("_", " ", "/", " ").Replace(ci.Text), strings.NewReplacer("_", " ", "/", " ").Replace(al)):
					newByUnderscore++
					continue
				case oldAliasSeen(strings.NewReplacer("-", " ", "‐", " ", "‑", " ").Replace(ci.Text), strings.ReplaceAll(al, "-", " ")):
					newByHyphen++
					continue
				}
				if len(exNew) < cap(exNew) {
					exNew = append(exNew, fmt.Sprintf("%-28s [%s]", cut(al, 28), cut(e.Name, 20)))
				}
			}
		}
	}
	pc := func(n int) string {
		if pairs == 0 {
			return "—"
		}
		return fmt.Sprintf("%7d  %5.2f%%", n, 100*float64(n)/float64(pairs))
	}
	fmt.Printf("\nА3.4. Сверка синонимов: `clean` сегодня против `SeenInText` (упоминаний %d, пар «синоним, кусок» %d, зерно %d)\n\n",
		len(picked), pairs, seed)
	fmt.Printf("  обе сверки видят синоним в куске        %s\n", pc(bothYes))
	fmt.Printf("  обе не видят                            %s\n", pc(bothNo))
	fmt.Printf("  видит ТОЛЬКО прежняя (новая отбросит)   %s\n", pc(onlyOld))
	fmt.Printf("      из них синонимы короче трёх знаков  %s\n", pc(onlyOldShort))
	fmt.Printf("  видит ТОЛЬКО новая (новая вернёт)       %s\n", pc(onlyNew))
	fmt.Printf("      «_» и «/» прочитаны пробелом         %s\n", pc(newByUnderscore))
	fmt.Printf("      дефис прочитан пробелом             %s\n", pc(newByHyphen))
	fmt.Printf("      починенный текст (перенос, лигатура) %s\n", pc(onlyNew-newByUnderscore-newByHyphen))
	if len(exNew) > 0 {
		fmt.Println("\n  примеры «вернёт новая» из-за починенного текста (синоним [понятие]):")
		for _, x := range exNew {
			fmt.Println("   ", x)
		}
	}
	if len(exOld) > 0 {
		fmt.Println("\n  примеры «отбросит новая», кроме коротких (синоним [понятие] ← начало куска):")
		for _, x := range exOld {
			fmt.Println("   ", x)
		}
	}
}
