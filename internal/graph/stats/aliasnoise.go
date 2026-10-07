package stats

// Ложные синонимы: раскрытия аббревиатур, утащившие понятие в чужую область
// (этап 103, Ш3.5; найдено уплотнением реестра 16.09.2026).
//
// **Случай, с которого началось.** У понятия `Relation extraction` синоним
// `RE` законный. Но `RE` — ещё и обычное сокращение `regular expressions`,
// и модель извлечения где-то приписала это второе раскрытие как синоним;
// следом прилипла «стандартная библиотека» (регулярные выражения в Go живут
// в стандартной библиотеке). Узел про извлечение отношений собрал связи
// стандартной библиотеки: `Go` (113 подтверждений), `fmt` (28), `HTTP`, `JSON`.
// Замер 15.09.2026 показывал у него 101 чужую связь из 133 (76%) — худший
// результат среди проверенных, и причина оказалась не в разнородности книг.
//
// **Первый признак отвергнут замером 16.09.2026.** Пробовалось: синоним
// подозрителен, если он — имя другого понятия и у пары нет ни одной общей
// книги. Дало 73 находки, и почти все оказались ВЕРНЫМИ переводами:
// `GPU ↔ видеокарта`, `ETL-пайплайн ↔ ETL pipeline`, `XSS-атаки ↔ xss attack`.
// Причина ясна задним числом: русская и английская книги — разные книги,
// общих у перевода с оригиналом может не быть вовсе. Настоящий случай
// (`Relation extraction ↔ стандартная библиотека`) признак не нашёл.
//
// **Признак, который считается здесь.** Ложное раскрытие видно не по книгам,
// а по самой аббревиатуре: у понятия есть синоним-аббревиатура `ABC`, имя
// понятия складывается в `ABC` по первым буквам слов — и среди синонимов
// лежит ДРУГАЯ фраза, которая тоже складывается в `ABC`. Значит у одной
// аббревиатуры два разных раскрытия:
//
//   `Relation extraction` + `RE` + `regular expressions` (r+e = RE) — конфликт;
//   `GPU` + `видеокарта` — буквы не складываются, это перевод, не раскрытие.
//
// Прежний признак (было):
//   1. X — собственное имя другого живого понятия B (в реестре такое есть);
//   2. книги A и B **не пересекаются вовсе** — то есть ни одна книга не
//      говорит о них обоих, а значит синонимия неоткуда взяться;
//   3. у A есть синоним-аббревиатура (2–5 заглавных знаков) — через неё
//      раскрытие и приходит.
//
// Пункт 2 здесь главный: похожесть имён ничего не доказывает, а общая книга —
// доказывает. `goroutine ↔ горутина` живут в одних книгах; `Relation
// extraction ↔ стандартная библиотека` — ни в одной.
//
// Ничего не меняет: читает граф и печатает. Снятие синонимов — отдельная
// работа и отдельное слово.

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
)

// aliasSuspect — подозрительный синоним и его цена.
type aliasSuspect struct {
	owner     string // понятие, у которого висит синоним
	ownerID   uint32
	alias     string // сам синоним
	otherName string // понятие, чьим именем он является
	abbrev    string // аббревиатура, через которую пришло раскрытие
	shared    int    // общих книг у пары (0 — главный признак)
	ownerDeg  int    // связей у понятия-владельца
}

// isAbbrev — похоже ли слово на аббревиатуру: 2–5 знаков, все заглавные.
//
// Цифры допускаются (`A2A`, `S3`), строчные — нет: `Go` аббревиатурой
// не считается, иначе под подозрение попадёт половина языков.
func isAbbrev(s string) bool {
	r := []rune(strings.TrimSpace(s))
	if len(r) < 2 || len(r) > 5 {
		return false
	}
	letters := 0
	for _, c := range r {
		if unicode.IsLetter(c) {
			if !unicode.IsUpper(c) {
				return false
			}
			letters++
		} else if !unicode.IsDigit(c) {
			return false
		}
	}
	return letters >= 2
}

// initialsOf складывает первые буквы слов фразы: «regular expressions» → «RE».
//
// Дефис и подчёркивание считаются границей слова («multi-query» → «MQ»),
// служебные слова не выбрасываются: их выбрасывание — догадка, а лишняя буква
// просто не даст совпадения, то есть ошибётся в безопасную сторону.
func initialsOf(s string) string {
	var b strings.Builder
	prevSep := true
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if prevSep {
				b.WriteRune(unicode.ToUpper(r))
			}
			prevSep = false
		default:
			prevSep = true
		}
	}
	return b.String()
}

func aliasNoise(g *graph.Graph, limit int) {
	live := g.Entities().Live()
	fmt.Printf("\nШ3.5. Ложные раскрытия аббревиатур: понятий живых %d\n", len(live))

	// Книги понятия — по журналу упоминаний: поле Docs в реестре не заполняется.
	booksOf := func(id uint32) map[uint32]bool {
		out := map[uint32]bool{}
		for _, k := range g.Mentions().Of(id) {
			out[k.Doc] = true
		}
		return out
	}

	var found []aliasSuspect
	withAbbrev := 0
	for _, e := range live {
		aliases := g.Entities().DisplayAliases(e)
		if len(aliases) == 0 {
			continue
		}
		abbrev := ""
		for _, a := range aliases {
			if isAbbrev(a) {
				abbrev = a
				break
			}
		}
		if abbrev == "" {
			continue // раскрытию аббревиатуры взяться неоткуда
		}
		withAbbrev++

		// Имя понятия само складывается в свою аббревиатуру — это норма
		// и точка отсчёта: именно от него отличаются чужие раскрытия.
		abbrevUp := strings.ToUpper(strings.TrimSpace(abbrev))
		if initialsOf(e.Name) != abbrevUp {
			continue // имя в аббревиатуру не складывается — сравнивать не с чем
		}
		for _, a := range aliases {
			if isAbbrev(a) || strings.EqualFold(a, e.Name) {
				continue
			}
			if initialsOf(a) != abbrevUp {
				continue // не раскрытие этой аббревиатуры
			}
			// Чьё это имя и есть ли общие книги — не признак, а справка человеку:
			shared, otherName := 0, ""
			if other, ok := g.Entities().Lookup(a); ok && other.ID != e.ID {
				otherName = other.Name
				mine := booksOf(e.ID)
				for doc := range booksOf(other.ID) {
					if mine[doc] {
						shared++
					}
				}
			}
			found = append(found, aliasSuspect{
				owner: e.Name, ownerID: e.ID, alias: a,
				otherName: otherName, abbrev: abbrev,
				shared: shared, ownerDeg: len(g.Edges().Neighbors(e.ID)),
			})
		}
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].ownerDeg != found[j].ownerDeg {
			return found[i].ownerDeg > found[j].ownerDeg
		}
		return found[i].owner < found[j].owner
	})

	fmt.Printf("  понятий с синонимом-аббревиатурой: %d\n", withAbbrev)
	fmt.Printf("  из них подозрительных синонимов: %d\n\n", len(found))
	if len(found) == 0 {
		return
	}
	fmt.Println("  понятие (связей)              аббрев  ложный синоним → чьё это имя")
	for i, s := range found {
		if i >= limit {
			fmt.Printf("  …и ещё %d\n", len(found)-limit)
			break
		}
		fmt.Printf("  %-28s %-7s %s → %s\n",
			cut(s.owner, 28)+" ("+fmt.Sprint(s.ownerDeg)+")", cut(s.abbrev, 7),
			cut(s.alias, 26), cut(s.otherName, 26))
	}
	fmt.Println()
	fmt.Println("  «Подозрительный» — синоним, складывающийся в ту же аббревиатуру,")
	fmt.Println("  что и имя понятия, но самим именем не являющийся: у одной аббревиатуры")
	fmt.Println("  два разных раскрытия, и одно может быть из чужой области.")
	fmt.Println("  Решает человек: `LLM` = `large language model` и `LLM system` — оба верны,")
	fmt.Println("  а `RE` = `relation extraction` и `regular expressions` — нет.")
}
