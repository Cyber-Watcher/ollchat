package stats

// Цена проверки имён: что отсечётся, если сверять с текстом не только
// синонимы, но и сами имена понятий (этап 104, П6.3).
//
// **Откуда задача.** Проверка «сказанное моделью должно стоять в куске»
// у нас есть (`clean` в `internal/graph/extract.go`, заведена 03.09.2026:
// 20,1% синонимов оказались выдумкой), но применяется **только к синонимам**.
// Имена понятий и концы связей не сверяются никогда — отсюда 2,9% связей,
// не видных в своём куске (замер `-provenance` 16.09.2026).
//
// Книги советуют сверять строкой: «factuality verification can be performed
// by using string matches» («Designing Large Language Model Applications»,
// Pai, 2025, стр. 276), а «Agentic RAG Systems» (Norman, 2026, стр. 123)
// показывает конвейер, где кусок, не прошедший валидацию, пропускается целиком.
//
// **Но включать проверку вслепую нельзя.** Модель законно нормализует имя:
// в тексте «goroutines», в графе `goroutine`. Строгая сверка отсекла бы
// верное вместе с выдумками, и цена этого — вот что здесь считается.
//
// Разряды, от самого мягкого совпадения к отсутствию:
//   дословно · без регистра · по основам слов · только синоним · НЕ НАЙДЕНО
//
// Ничего не меняет.

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// stemPhrase приводит фразу к основам слов — тем же способом, каким это
// делает поиск по словам, чтобы «goroutines» и «goroutine» сошлись.
func stemPhrase(s string) string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !isWordRune(r)
	})
	for i, w := range fields {
		fields[i] = kb.StemWord(w)
	}
	return strings.Join(fields, " ")
}

func isWordRune(r rune) bool {
	return r == '_' || r == '-' || r == '.' || r == '+' || r == '#' ||
		(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= 'а' && r <= 'я') || (r >= 'А' && r <= 'Я') || r == 'ё' || r == 'Ё'
}

// mentionRef — одно упоминание: понятие в куске.
type mentionRef struct {
	ent uint32
	key graph.ChunkKey
}

// sampleMentions выбирает n упоминаний равномерно ПО УПОМИНАНИЯМ, без повторов
// и воспроизводимо. Прежний способ («понятие, затем его упоминание») давал долю
// по понятиям: упоминание хаба попадало в выборку в тысячи раз реже упоминания
// одиночки, а вопрос замера — «какая доля упоминаний» (аудит 17.09.2026, Б14).
func sampleMentions(g *graph.Graph, n int, seed int64) []mentionRef {
	var all []mentionRef
	for _, e := range g.Entities().Live() { // по возрастанию номера
		for _, k := range g.Mentions().Of(e.ID) { // по возрастанию куска
			all = append(all, mentionRef{e.ID, k})
		}
	}
	if n >= len(all) {
		return all
	}
	rnd := rand.New(rand.NewSource(seed))
	for i := 0; i < n; i++ {
		j := i + rnd.Intn(len(all)-i)
		all[i], all[j] = all[j], all[i]
	}
	return all[:n]
}

// oldMentions — прежняя выборка «понятие, затем упоминание», для сравнения
// «до/после» одним бинарём (ключ -oldsample).
func oldMentions(g *graph.Graph, n int, seed int64) []mentionRef {
	live := g.Entities().Live()
	rnd := rand.New(rand.NewSource(seed))
	var out []mentionRef
	for tries := 0; len(out) < n && tries < n*20; tries++ {
		e := live[rnd.Intn(len(live))]
		keys := g.Mentions().Of(e.ID)
		if len(keys) == 0 {
			continue
		}
		out = append(out, mentionRef{e.ID, keys[rnd.Intn(len(keys))]})
	}
	return out
}

func nameCheck(g *graph.Graph, c *kb.Collection, sample int, seed int64, old bool) {
	var picked []mentionRef
	how := "равномерно по упоминаниям"
	if old {
		picked, how = oldMentions(g, sample, seed), "ПРЕЖНИЙ способ: понятие, затем его упоминание"
	} else {
		picked = sampleMentions(g, sample, seed)
	}

	var exact, noCase, byStem, aliasOnly, missing, checked int
	// Что сделал бы заслон формата 2 (`groundNames`): та же сверка, что в коде
	// сборки, — два чтения куска, дефис и подчёркивание как пробел, лигатуры.
	var guardSeen, guardRenamed, guardDropped int
	examples := make([]string, 0, 8)

	for _, m := range picked {
		e, ok := g.Entities().Get(m.ent)
		if !ok {
			continue
		}
		ci, ok := c.ChunkByRef(m.key.Doc, m.key.Ord)
		if !ok {
			continue
		}
		checked++

		joined, hyphened := graph.MatchText(ci.Text)
		switch {
		case graph.SeenInText(joined, hyphened, e.Name):
			guardSeen++
		default:
			renamed := false
			for _, al := range g.Entities().DisplayAliases(e) {
				if graph.SeenInText(joined, hyphened, al) {
					renamed = true
					break
				}
			}
			if renamed {
				guardRenamed++
			} else {
				guardDropped++
			}
		}

		switch {
		case strings.Contains(ci.Text, e.Name):
			exact++
		case strings.Contains(strings.ToLower(ci.Text), strings.ToLower(e.Name)):
			noCase++
		case strings.Contains(stemPhrase(ci.Text), stemPhrase(e.Name)):
			byStem++
		default:
			// Имени нет ни в каком виде — может, модель назвала понятие
			// синонимом, который в куске есть.
			found := false
			low := strings.ToLower(ci.Text)
			for _, al := range g.Entities().DisplayAliases(e) {
				if len([]rune(al)) >= 3 && strings.Contains(low, strings.ToLower(al)) {
					found = true
					break
				}
			}
			if found {
				aliasOnly++
				break
			}
			missing++
			if len(examples) < 8 {
				examples = append(examples, fmt.Sprintf("%-34s ← %s",
					cut(e.Name, 34), cut(oneLine(ci.Text), 90)))
			}
		}
	}

	pc := func(n int) string {
		if checked == 0 {
			return "—"
		}
		return fmt.Sprintf("%5d  %5.1f%%", n, 100*float64(n)/float64(checked))
	}
	fmt.Printf("\nП6.3. Цена проверки имён: упоминаний проверено %d (зерно %d; выборка: %s)\n\n", checked, seed, how)
	fmt.Printf("  имя стоит в куске дословно            %s\n", pc(exact))
	fmt.Printf("  совпало без учёта регистра            %s\n", pc(noCase))
	fmt.Printf("  совпало по основам слов (число, падеж) %s\n", pc(byStem))
	fmt.Printf("  имени нет, но есть его синоним        %s\n", pc(aliasOnly))
	fmt.Printf("  НЕ НАЙДЕНО ничего                     %s\n", pc(missing))
	if len(examples) > 0 {
		fmt.Println("\n  примеры «не найдено» (имя ← начало куска):")
		for _, x := range examples {
			fmt.Println("   ", x)
		}
	}
	fmt.Println()
	fmt.Println("  Цена строгой проверки (только дословно) — разряды «без регистра»,")
	fmt.Println("  «по основам» и «синоним»: столько ВЕРНЫХ упоминаний она выбросила бы.")
	fmt.Println("  Цена мягкой (дословно + регистр + основы) — один разряд «синоним».")
	fmt.Println("  Разряд «НЕ НАЙДЕНО» — то, ради чего проверку и заводят.")
	fmt.Println()
	fmt.Println("  Заслон формата 2 (та же сверка, что в сборке опытного графа):")
	fmt.Printf("    имя видно в куске                     %s\n", pc(guardSeen))
	fmt.Printf("    переименовал бы по синониму из текста %s\n", pc(guardRenamed))
	fmt.Printf("    ОТБРОСИЛ бы понятие                   %s\n", pc(guardDropped))
}
