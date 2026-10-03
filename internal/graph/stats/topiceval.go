package stats

// Тема как признак уместности понятия входа — проверка книжного приёма
// (этап 89, 16.09.2026).
//
// **Откуда мысль.** «Essential GraphRAG» (Bratanic, Hane, 2025, стр. 123):
// «Local search retrieves information from entities closely connected within
// a detected community» — «локальный поиск берёт сведения у понятий, тесно
// связанных внутри обнаруженного сообщества». То есть MS GraphRAG отбирает
// понятия НЕ по одной лишь близости к вопросу, а внутри найденной темы.
//
// У нас вход берёт шесть понятий по слову и смыслу, и в какой они теме —
// не смотрит вовсе. Отсюда и картина замера 15.09: на вопрос «как оценивать
// качество графа» приходят `quality`, `Metrics`, `Authorization`, `DevOps` —
// понятия из разных тем, и уже их связи тащат Prometheus с kubelet.
//
// **Что здесь считается.** Для каждого вопроса: в каких темах лежат понятия
// входа, есть ли преобладающая тема, и — главное — **отличается ли доля
// связей из чужих каталогов у понятий преобладающей темы от доли у остальных**.
// Если отличается сильно, тема годится в признак уместности, и правка входа
// понятна. Если нет — приём книги нам не подходит, и это тоже ответ.
//
// Ничего не меняет: читает граф и печатает числа. Карта нужна только
// на вектор вопроса.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

// level — на каком уровне разбиения искать общую тему. 0 — мелкие темы,
// 1 — объединения. У нас 55 988 мелких тем на 264 тысячи понятий, то есть
// пять понятий на тему: два понятия входа почти никогда не окажутся в одной,
// и приём книги на этом уровне не проверяется вовсе.
// readOwnFolders — «своя область» каждого вопроса по chunk_id набора:
// кусок принадлежит книге, книга лежит в каталоге, каталог и есть область.
// Набор без chunk_id (методологический) даёт пустую карту — там область одна
// на всех и приходит ключом.
func readOwnDocs(path string) map[string]uint32 {
	raw, err := os.ReadFile(path)
	die(err)
	out := map[string]uint32{}
	for _, m := range caseQueryChunkRe.FindAllStringSubmatch(string(raw), -1) {
		var doc uint64
		// chunk_id вида «books/243#163»: номер книги — между «/» и «#».
		if _, err := fmt.Sscanf(m[2], "books/%d#", &doc); err == nil {
			out[m[1]] = uint32(doc)
		}
	}
	return out
}

// caseQueryChunkRe достаёт из случая набора пару «вопрос → кусок».
var caseQueryChunkRe = regexp.MustCompile(`(?s)query\s*=\s*"([^"]+)".*?chunk_id\s*=\s*"([^"]+)"`)

func topicEval(cfg *config.Config, g *graph.Graph, c *kb.Collection, path, want string, level int) {
	questions := readMethodQuestions(path)
	if len(questions) == 0 {
		fmt.Println("в наборе не нашлось строк вида text = \"…\" или query = \"…\"")
		return
	}
	// Своя область — у каждого вопроса своя, если набор её знает.
	//
	// **Иначе замер врёт.** `graph_relations.toml` разноплановый: «Как связаны
	// fmt и strings» — про Go, «PCAP и tcpdump» — про сети. Считать своим
	// каталогом `/AI` для всех значит записать в «чужие» правильные связи
	// и получить 93% там, где на деле мера просто не о том (поймано 16.09.2026
	// на первом же прогоне: числа вышли обратные, и я чуть не выдал их за вывод).
	ownDoc := readOwnDocs(path)
	coms, err := g.LoadCommunities()
	if err != nil || coms == nil {
		fmt.Println("разбиение на темы не прочитано — считать нечего:", err)
		return
	}

	byDoc := map[uint32]string{}
	roots := c.Roots()
	for _, b := range c.MatchingDocs(kb.ChunkFilter{}) {
		byDoc[b.ID] = folderOf(b.Path, roots)
	}

	deps := find.Deps{
		Coll:     c,
		Graph:    g,
		Embedder: kbembed.New(cfg.KB.EmbedOptions(), cfg.EmbedFallback(), 2*time.Minute, nil),
	}

	fmt.Printf("\nТема как признак уместности понятия входа (этап 89; своя область — %s)\n", want)
	lvl := 0
	for _, com := range coms.List {
		if com.Level == level {
			lvl++
		}
	}
	fmt.Printf("  вопросов %d, тем уровня %d: %d (всего в разбиении %d)\n\n",
		len(questions), level, lvl, len(coms.List))

	// Счётчики по двум половинам: понятия преобладающей темы и все прочие.
	var mainRel, mainAlien, restRel, restAlien, noMainRel, noMainAlien int
	var mainAlienOld, restAlienOld int // прежняя мера: чужая ПЕРВАЯ выдержка
	wordsOnly := 0
	ownFolder := readQuestionFolders(path)
	var withMain, noMain int
	fmt.Println("  понятий  тем  в главной  вопрос")

	for _, q := range questions {
		res, err := find.Search(context.Background(), deps, q, find.Opts{})
		if err != nil || len(res.Entities) == 0 {
			continue
		}
		// Тема каждого понятия входа. Уровень 0: мелкие темы — именно они
		// говорят о предмете, объединения слишком общи.
		topicOf := map[string]int{}
		count := map[int]int{}
		for _, e := range res.Entities {
			// Of отдаёт МЕЛКУЮ тему (уровень 0); до объединения поднимаемся
			// через Parent. Без этого на уровне 1 не находилось ничего вовсе,
			// и замер молча показывал ноль тем у каждого вопроса.
			com, ok := coms.Of(e.ID)
			if !ok {
				continue
			}
			id := com.ID
			if level > com.Level {
				id = com.Parent
				if id < 0 {
					continue
				}
			}
			topicOf[e.Name] = id
			count[id]++
		}
		main, best := -1, 0
		for id, n := range count {
			if n > best || (n == best && id < main) {
				main, best = id, n
			}
		}
		if best < 2 {
			// Преобладающей темы нет: все понятия из разных тем. Такой вопрос
			// приём книги не покрывает — считаем отдельно, а не приписываем
			// к какой-нибудь половине.
			noMain++
		} else {
			withMain++
		}
		fmt.Printf("  %7d %4d %10d  %s\n", len(res.Entities), len(count), best, cut(q, 44))

		if res.WordsOnly {
			wordsOnly++
		}
		mine := want
		if f := ownFolder[q]; f != "" {
			mine = f
		}
		if doc, ok := ownDoc[q]; ok {
			if f := byDoc[doc]; f != "" {
				mine = f
			}
		}
		for _, rel := range res.Relations {
			first, all := alienRelation(rel, byDoc, mine)
			alien, alienOld := 0, 0
			if all {
				alien = 1
			}
			if first {
				alienOld = 1
			}
			// Понятие входа — любой из концов: входящая связь печатается
			// «сосед → понятие», и понятие входа стоит в ней вторым. Наличие
			// темы проверяется явно: у понятия без темы в карте ноль, и оно
			// засчитывалось бы теме с номером 0 (аудит 17.09.2026, S12).
			ts, okS := topicOf[rel.Src]
			td, okD := topicOf[rel.Dst]
			switch {
			case best < 2:
				// Вопрос без преобладающей темы — отдельно, как и обещано
				// выше: в «остальных» его связи разбавляли половину, с которой
				// сравнивается главная тема.
				noMainRel++
				noMainAlien += alien
			case (okS && ts == main) || (okD && td == main):
				mainRel++
				mainAlien += alien
				mainAlienOld += alienOld
			default:
				restRel++
				restAlien += alien
				restAlienOld += alienOld
			}
		}
	}

	share := func(a, n int) string {
		if n == 0 {
			return "—"
		}
		return fmt.Sprintf("%d из %d (%.1f%%)", a, n, 100*float64(a)/float64(n))
	}
	fmt.Println()
	fmt.Printf("  вопросов с преобладающей темой: %d, без неё: %d\n", withMain, noMain)
	fmt.Printf("  связи понятий ГЛАВНОЙ темы,  чужих: %s\n", share(mainAlien, mainRel))
	fmt.Printf("  связи всех остальных понятий, чужих: %s\n", share(restAlien, restRel))
	fmt.Printf("  связи вопросов без главной темы, чужих: %s — в сравнение не входят\n", share(noMainAlien, noMainRel))
	fmt.Printf("  прежняя мера (первая выдержка): главная тема %s; остальные %s\n",
		share(mainAlienOld, mainRel), share(restAlienOld, restRel))
	if wordsOnly > 0 {
		fmt.Printf("  ВНИМАНИЕ: у %d вопросов смысловой вход не отработал — с прежними числами не сравнивать\n", wordsOnly)
	}
	fmt.Println()
	fmt.Println("  Если у понятий главной темы чужих заметно меньше, тема годится")
	fmt.Println("  в признак уместности: вход сможет отбрасывать случайные понятия,")
	fmt.Println("  а не только ранжировать их по близости. Если разницы нет — приём")
	fmt.Println("  «local search внутри сообщества» нам не подходит, и это тоже ответ.")
}
