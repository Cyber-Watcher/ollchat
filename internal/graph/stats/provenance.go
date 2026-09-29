package stats

// Целостность провенанса: у каждой ли связи есть живой кусок-источник
// (находка из книги, 16.09.2026).
//
// **Откуда.** «Agentic RAG Systems» (Norman, 2026, стр. 129) перечисляет
// метрики, по которым за графом надо следить в работе:
//
//   > Production monitoring tracks several graph-specific metrics: entity
//   > duplication rates, relationship coverage, **provenance integrity** (how
//   > often relationships have valid source chunks), and community stability.
//
//   > Наблюдение в работе следит за несколькими метриками графа: долей
//   > двойников, покрытием связями, **целостностью провенанса** (как часто
//   > у связей есть годные куски-источники) и устойчивостью сообществ.
//
// И там же — почему это не роскошь:
//
//   > A graph that silently degrades … produces a retrieval system that becomes
//   > gradually wrong without anything appearing broken.
//
//   > Граф, который тихо портится… даёт поиск, постепенно становящийся
//   > неверным, причём ничто не выглядит поломанным.
//
// **Из четырёх метрик у нас есть три:** доля двойников (доктор: поглощено
// склейкой), понятия без связей (4%) и устойчивость тем (`--graph-drift`).
// Целостность провенанса не проверял никто: доктор считает ЧИСЛО подтверждений,
// но не смотрит, существует ли кусок, на который они указывают.
//
// **Что здесь считается.** Выборка связей; у каждой берётся Evidence
// (книга + номер куска) и проверяется, что такой кусок в коллекции есть,
// не отброшен вместе с книгой и что оба имени связи в нём действительно
// встречаются. Последнее строже целостности: ссылка может быть живой,
// а кусок — не о том.

import (
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// showText — сколько неподтверждённых связей показать С ТЕКСТОМ куска.
//
// Без текста разобрать причину нельзя: доля в процентах не отличает выдумку
// модели от кореференции («эта библиотека») и от каши из битого PDF.
// Разбор глазами — этап 104, П6.1.
var showText int

func provenanceEval(g *graph.Graph, c *kb.Collection, sample int, seed int64) {
	live := g.Entities().Live()
	if len(live) == 0 {
		fmt.Println("в графе нет живых понятий")
		return
	}
	var checked, missing, dropped, noBoth, noOne int
	// Второй счёт на ТОЙ ЖЕ выборке — общей функцией сверки graph.SeenInText
	// (склейка переносов, лигатуры, границы слов). Короткие имена (< 3 знаков)
	// ею не проверяются и считаются отдельно.
	var both2, one2, none2, short2 int
	examples := make([]string, 0, 6)

	// Выборка равномерна по записям связей (graph.SampleEdges), а не
	// «понятие → его связь» (аудит 17.09.2026, S9).
	for _, ed := range g.SampleEdges(sample, seed) {
		e, ok := g.Entities().Get(ed.Src)
		if !ok {
			continue
		}
		dst, ok := g.Entities().Get(ed.Dst)
		if !ok {
			continue
		}
		checked++

		if g.Dropped().Dropped(ed.Evidence.Doc) {
			dropped++
			continue
		}
		ci, ok := c.ChunkByRef(ed.Evidence.Doc, ed.Evidence.Ord)
		if !ok {
			missing++
			if len(examples) < 6 {
				examples = append(examples, fmt.Sprintf("нет куска %s: %s → %s",
					ed.Evidence.String(), cut(e.Name, 26), cut(dst.Name, 26)))
			}
			continue
		}
		// Ищем не только имя, но и синонимы: граф двуязычный, и модель
		// извлечения законно называет понятие тем написанием, которого
		// в этом куске нет («горутина» против «goroutine»). Без синонимов
		// замер записал бы законную связь в выдумки — первый прогон
		// 16.09.2026 дал 6,73% «ни одного имени» именно так.
		low := strings.ToLower(ci.Text)
		seen := func(ent graph.Entity) bool {
			if strings.Contains(low, strings.ToLower(ent.Name)) {
				return true
			}
			for _, al := range g.Entities().DisplayAliases(ent) {
				if len(al) >= 3 && strings.Contains(low, strings.ToLower(al)) {
					return true
				}
			}
			return false
		}
		a := seen(e)
		b := seen(dst)
		{
			joined, hyph := graph.MatchText(ci.Text)
			seen2 := func(ent graph.Entity) (found, short bool) {
				if len([]rune(graph.MatchName(ent.Name))) < 3 {
					short = true
				}
				if graph.SeenInText(joined, hyph, ent.Name) {
					return true, short
				}
				for _, al := range g.Entities().DisplayAliases(ent) {
					if graph.SeenInText(joined, hyph, al) {
						return true, short
					}
				}
				return false, short
			}
			a2, sa := seen2(e)
			b2, sb := seen2(dst)
			switch {
			case (sa && !a2) || (sb && !b2):
				short2++
			case a2 && b2:
				both2++
			case a2 || b2:
				one2++
			default:
				none2++
			}
		}
		switch {
		case a && b:
			// Связь подтверждена буквально: оба имени в куске есть.
		case a || b:
			noOne++
			if len(examples) < 6 {
				examples = append(examples, fmt.Sprintf("в куске только одно имя: %s → %s (%s)",
					cut(e.Name, 22), cut(dst.Name, 22), ed.Evidence.String()))
			}
		default:
			noBoth++
			if len(examples) < 6 {
				examples = append(examples, fmt.Sprintf("в куске нет ни одного имени: %s → %s (%s)",
					cut(e.Name, 22), cut(dst.Name, 22), ed.Evidence.String()))
			}
			if showText > 0 {
				showText--
				fmt.Printf("\n── %s → %s (%s, книга «%s»)\n",
					e.Name, dst.Name, ed.Evidence.String(), cut(ci.Book.Title, 46))
				fmt.Printf("   синонимы: %s | %s\n",
					cut(strings.Join(g.Entities().DisplayAliases(e), ", "), 60),
					cut(strings.Join(g.Entities().DisplayAliases(dst), ", "), 60))
				fmt.Printf("   кусок: %s\n", cut(oneLine(ci.Text), 700))
			}
		}
	}

	pc := func(n int) string { return fmt.Sprintf("%d (%.2f%%)", n, 100*float64(n)/float64(checked)) }
	fmt.Printf("\nЦелостность провенанса: проверено связей %d (выборка, зерно %d)\n\n", checked, seed)
	fmt.Printf("  кусок-источник не найден вовсе:       %s\n", pc(missing))
	fmt.Printf("  книга отброшена (это норма):          %s\n", pc(dropped))
	fmt.Printf("  оба имени в куске есть:               %s\n", pc(checked-missing-dropped-noOne-noBoth))
	fmt.Printf("  в куске только одно имя связи:        %s\n", pc(noOne))
	fmt.Printf("  в куске нет ни одного имени:          %s\n", pc(noBoth))
	fmt.Printf("\n  ТА ЖЕ выборка общей функцией сверки (переносы, лигатуры, границы слов):\n")
	fmt.Printf("  оба имени в куске есть:               %s\n", pc(both2))
	fmt.Printf("  в куске только одно имя связи:        %s\n", pc(one2))
	fmt.Printf("  в куске нет ни одного имени:          %s\n", pc(none2))
	fmt.Printf("  короткое имя (< 3 знаков), не судим:  %s\n", pc(short2))
	if len(examples) > 0 {
		fmt.Println("\n  примеры:")
		for _, x := range examples {
			fmt.Println("   ", x)
		}
	}
	fmt.Println()
	fmt.Println("  «Кусок не найден» — настоящая беда: выдержка не покажется, а")
	fmt.Println("  доктор об этом молчит (он считает число подтверждений, не проверяя")
	fmt.Println("  ссылки). «Только одно имя» и «ни одного» — не ошибка ссылки,")
	fmt.Println("  а либо синоним (модель писала другое написание), либо ошибка")
	fmt.Println("  извлечения: связь построена по куску, где её не видно.")
}
