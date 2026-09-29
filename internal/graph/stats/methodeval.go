package stats

// Замер входа на МЕТОДОЛОГИЧЕСКИХ вопросах — этап 89 (15.09.2026).
//
// Прежний набор (`graph_relations.toml`) состоит из вопросов «как связаны X и Y»,
// где оба понятия названы явно: по ним вход работает на 100% (60 из 60, место
// 1.50). Но мусор в выдаче виден не на них, а на вопросах БЕЗ явных понятий:
// «как оценивать качество графа» приводит понятие `quality` (60 упоминаний
// в 29 книгах), и уже ЕГО связи тянут `Authorization`, `Metrics`, `DevOps`.
//
// Мерить такие вопросы прежним способом нельзя — нет двух правильных понятий,
// с которыми сверяться. Мера здесь другая: **из каких каталогов библиотеки
// приходят подтверждения связей в выдаче**. Вопрос про графы должен приводить
// к `/AI`, а не к `/DevOps`.
//
// Карта нужна только на вектор вопроса (bge-m3), граф открывается один раз
// на весь набор — по вопросу отдельным запуском это стоило бы 5 минут на каждый.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

var methodQuestionRe = regexp.MustCompile(`(?m)^\s*text\s*=\s*"([^"]+)"`)

// methodEval прогоняет набор вопросов и считает, откуда пришли подтверждения.
func methodEval(cfg *config.Config, g *graph.Graph, c *kb.Collection, path, want string) {
	raw, err := os.ReadFile(path)
	die(err)
	var questions []string
	for _, m := range methodQuestionRe.FindAllStringSubmatch(string(raw), -1) {
		questions = append(questions, m[1])
	}
	if len(questions) == 0 {
		fmt.Println("в наборе не нашлось строк вида text = \"…\"")
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
	opts := find.Opts{}

	fmt.Printf("\nЗамер входа на методологических вопросах (этап 89; своя область — %s)\n", want)
	fmt.Printf("  вопросов: %d, набор: %s\n\n", len(questions), path)
	fmt.Println("  понятий  связей  чужих  вопрос")

	var totalRel, totalAlien, alienQuestions int
	worst := make([]string, 0, 4)
	for _, q := range questions {
		res, err := find.Search(context.Background(), deps, q, opts)
		if err != nil {
			fmt.Printf("  %-8s %-7s %-6s %s — ошибка: %v\n", "-", "-", "-", cut(q, 42), err)
			continue
		}
		alien := 0
		for _, rel := range res.Relations {
			if byDoc[rel.Evidence.Doc] != want {
				alien++
			}
		}
		totalRel += len(res.Relations)
		totalAlien += alien
		share := 0.0
		if len(res.Relations) > 0 {
			share = 100 * float64(alien) / float64(len(res.Relations))
		}
		if share >= 30 {
			alienQuestions++
			if len(worst) < 4 {
				worst = append(worst, fmt.Sprintf("%s (чужих %d из %d)", q, alien, len(res.Relations)))
			}
		}
		fmt.Printf("  %7d %7d %5d%%  %s\n", len(res.Entities), len(res.Relations),
			int(share), cut(q, 42))
	}

	fmt.Println()
	if totalRel == 0 {
		fmt.Println("  связей в выдаче не было вовсе")
		return
	}
	fmt.Printf("  ИТОГО связей %d, из чужих каталогов %d (%.1f%%)\n",
		totalRel, totalAlien, 100*float64(totalAlien)/float64(totalRel))
	fmt.Printf("  вопросов, где чужих больше 30%%: %d из %d\n", alienQuestions, len(questions))
	if len(worst) > 0 {
		fmt.Println("\n  Хуже всего:")
		for _, w := range worst {
			fmt.Printf("    %s\n", w)
		}
	}
	fmt.Println()
	fmt.Println("  Связь считается чужой по каталогу книги, подтвердившей её. Это не")
	fmt.Println("  ошибка графа: в книге по Kubernetes `Metrics` действительно связан")
	fmt.Println("  с Prometheus. Вопрос в том, уместно ли это в ответе про графы.")
	_ = strings.TrimSpace
}
