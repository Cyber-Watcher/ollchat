package stats

// Обзор тем против входа по понятиям на ТЕМАТИЧЕСКИХ вопросах
// (этап 89, находка Н12 от 16.09.2026).
//
// **Зачем.** «Designing Large Language Model Applications» (Pai, 2025,
// стр. 318–319): обычные приёмы поиска плохо отвечают на вопросы, обобщающие
// темы набора, — для них и заведены описания сообществ. Наш методологический
// набор состоит ровно из таких вопросов («как оценивать качество графа
// знаний»), а мерили мы на них вход по понятиям и получили 26,9% связей
// из чужих каталогов.
//
// Здесь сравниваются два пути на ОДНИХ вопросах:
//   вход по понятиям — доля связей, подтверждённых книгами чужой области;
//   обзор тем — доля понятий найденных тем, пришедших из чужой области.
//
// **Мера одна и та же — откуда материал**, потому что сравнивать «качество
// ответа» без судьи нельзя, а происхождение материала считается точно.
//
// Ничего не меняет: читает граф, печатает числа.

import (
	"context"
	"fmt"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

func overviewEval(cfg *config.Config, g *graph.Graph, c *kb.Collection, path, want string, topics int) {
	questions := readMethodQuestions(path)
	if len(questions) == 0 {
		fmt.Println("в наборе не нашлось вопросов")
		return
	}
	coms, err := g.LoadCommunities()
	if err != nil || coms == nil {
		fmt.Println("разбиение на темы не прочитано:", err)
		return
	}

	byDoc := map[uint32]string{}
	roots := append(append([]string{}, cfg.KB.Roots...), c.Roots()...) // корни библиотеки, затем коллекции
	for _, b := range c.MatchingDocs(kb.ChunkFilter{}) {
		byDoc[b.ID] = folderOf(b.Path, roots)
	}
	// Область понятия — по книгам, где оно встречается: преобладающий каталог.
	folderOfEntity := func(id uint32) string {
		best := mainFolder(g, byDoc, id)
		return best
	}

	deps := find.Deps{
		Coll:     c,
		Graph:    g,
		Embedder: kbembed.New(cfg.KB.EmbedOptions(), cfg.EmbedFallback(), 2*time.Minute, nil),
	}

	// Своя область — из набора вопросов (поле folder), если оно там есть;
	// ключ -domainnoise-folder — запасное значение (20.09.2026: набор по
	// /SoftArch мерился против умолчания /AI, и все понятия вышли «чужими»).
	ownFolder := readQuestionFolders(path)
	fmt.Printf("\nОбзор тем против входа по понятиям (этап 89, Н12; своя область — из набора, иначе %s)\n", want)
	fmt.Printf("  вопросов %d, тем показываем %d\n\n", len(questions), topics)
	fmt.Println("  вход: связей  чужих | обзор: тем  понятий  чужих | вопрос")

	var relAll, relAlien, entAll, entAlien, noTopics int
	for _, q := range questions {
		own := want
		if f := ownFolder[q]; f != "" {
			own = f
		}
		res, err := find.Search(context.Background(), deps, q, find.Opts{})
		if err != nil {
			continue
		}
		relA := 0
		for _, rel := range res.Relations {
			if byDoc[rel.Evidence.Doc] != own {
				relA++
			}
		}
		relAll += len(res.Relations)
		relAlien += relA

		// Тот же вопрос — через обзор тем. Вектор вопроса берём у поиска:
		// считать его второй раз значит платить картой дважды за одно.
		// QueryVector отдаёт три значения: вектор, эмбеддер и оговорку.
		// Нужен только вектор — остальное для сообщений пользователю.
		qv, _, _ := find.QueryVector(context.Background(), deps, q, find.Opts{})
		ov := g.Overview(q, coms, graph.OverviewOpts{
			TopCommunities: topics,
			QueryVector:    qv,
		})
		entA, entN := 0, 0
		for _, t := range ov.Topics {
			// Смотрим первые понятия темы: они и есть то, о чём она (Members
			// отсортированы по числу упоминаний).
			for i, id := range t.Members {
				if i >= 10 {
					break
				}
				entN++
				if folderOfEntity(id) != own {
					entA++
				}
			}
		}
		if len(ov.Topics) == 0 {
			noTopics++
		}
		entAll += entN
		entAlien += entA
		fmt.Printf("  %12d %7d | %10d %8d %6d | %s\n",
			len(res.Relations), relA, len(ov.Topics), entN, entA, cut(q, 34))
	}

	share := func(a, n int) string {
		if n == 0 {
			return "—"
		}
		return fmt.Sprintf("%d из %d (%.1f%%)", a, n, 100*float64(a)/float64(n))
	}
	fmt.Println()
	fmt.Printf("  вход по понятиям: чужих связей   %s\n", share(relAlien, relAll))
	fmt.Printf("  обзор тем:        чужих понятий  %s\n", share(entAlien, entAll))
	if noTopics > 0 {
		fmt.Printf("  вопросов, где обзор не дал ни одной темы: %d\n", noTopics)
	}
	fmt.Println()
	fmt.Println("  Мера одна: откуда пришёл материал. Если у обзора чужого заметно")
	fmt.Println("  меньше, то для тематических вопросов надо подмешивать темы,")
	fmt.Println("  а не понятия, — и это правка подмеса, а не входа.")
	fmt.Println("  Осторожно: у обзора и входа разные единицы (понятия против")
	fmt.Println("  связей), поэтому сравнивать можно ДОЛИ, но не абсолютные числа.")
}
