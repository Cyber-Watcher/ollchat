package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
)

// Команда /search — поиск по графу и книгам без обращения к модели.
//
// **Почему в фоне, а не прямо в Update.** Открытие графа стоит 11.5 с (замер
// 29.08.2026 на четверти библиотеки), вектор вопроса — до kb.query_timeout.
// Держать на это цикл событий нельзя: интерфейс замирает, нажатия копятся,
// и человек жмёт Enter второй раз, решив, что команда не сработала. Образец —
// graphStatus, где это уже случилось и было исправлено.
//
// **Устаревшая выдача отбрасывается.** Пока считается первый поиск, человек
// успевает задать второй; ответ на первый, пришедший вторым, хуже, чем никакой.

// maxSearchTopK — потолок числа выдержек. Больше полусотни кусков в ленте
// человек не читает, а при -f это ещё и десятки экранов текста.
const maxSearchTopK = 50

// searchArgs — разобранные доводы команды.
type searchArgs struct {
	Query  string
	TopK   int
	Full   bool // куски целиком
	Rerank bool // вторая ступень отбора
}

const searchUsage = `использование: /search [-f] [-r] [N] <текст>
  -f, --full   показать куски целиком, а не короткими выдержками
  -r           вторая ступень отбора (реранкер), если он настроен
  N            сколько выдержек показать
Примеры:
  /search как связаны RAG и дообучение
  /search -f 3 goroutine scheduler`

// parseSearchArgs разбирает доводы команды.
//
// Голое число считается количеством, **только если за ним есть текст**:
// «/search 2026» — это запрос про 2026 год, а не просьба показать
// две тысячи двадцать шесть выдержек.
func parseSearchArgs(arg string, defTopK int) (searchArgs, error) {
	out := searchArgs{TopK: defTopK}
	fields := strings.Fields(arg)
	i := 0
	for ; i < len(fields); i++ {
		f := fields[i]
		switch {
		case f == "-f" || f == "--full" || f == "-ц":
			out.Full = true
			continue
		case f == "-r" || f == "--rerank":
			out.Rerank = true
			continue
		case strings.HasPrefix(f, "-"):
			return out, fmt.Errorf("неизвестный ключ %s\n%s", f, searchUsage)
		}
		if n, err := strconv.Atoi(f); err == nil && i+1 < len(fields) {
			if n <= 0 {
				return out, fmt.Errorf("сколько выдержек показать — число больше нуля\n%s", searchUsage)
			}
			out.TopK = n
			continue
		}
		break
	}
	out.Query = strings.TrimSpace(strings.Join(fields[i:], " "))
	if out.Query == "" {
		return out, fmt.Errorf("что искать?\n%s", searchUsage)
	}
	if out.TopK > maxSearchTopK {
		out.TopK = maxSearchTopK
	}
	return out, nil
}

// searchDoneMsg — готовая выдача поиска.
type searchDoneMsg struct {
	// src — источник кусков для выдержки под связью. Коллекция, по которой
	// искали: брать её из модели в момент отрисовки нельзя, человек мог
	// успеть переключить коллекцию командой /kb use.
	src  graph.Chunks
	gen  int
	res  find.Result
	full bool
	err  error
	note string // оговорка перед выдачей: реранкер не настроен и подобное
}

// searchCmd запускает поиск по графу и книгам.
func (m *Model) searchCmd(arg string) tea.Cmd {
	return m.searchCmdFor(arg, "", true, "search", "/search")
}

// searchCmdFor — общий ход /search и /kb search: одно ядро, один вид выдачи
// (этап 91, R2.7). collName — коллекция, пусто — выбранная; withGraph —
// входить ли в граф; mode — метка для журнала шагов; where — имя команды
// для ошибок.
func (m *Model) searchCmdFor(arg, collName string, withGraph bool, mode, where string) tea.Cmd {
	liveTopK, liveMaxPerDoc, liveMinCos, liveSemWeight := m.live.KB()
	a, err := parseSearchArgs(arg, liveTopK)
	if err != nil {
		m.fail(where, err)
		return nil
	}
	coll, err := m.kbCollection(collName)
	if err != nil {
		m.fail(where, err)
		return nil
	}

	// Реранкер: молчать о том, что ключ не сработал, нельзя — человек решит,
	// что вторая ступень отработала, и сравнит несравнимое.
	var rr kb.Reranker
	note := ""
	if a.Rerank {
		if r := kbrerank.New(m.cfg.KB.RerankOptions()); r != nil {
			rr = r
		} else {
			a.Rerank = false
			note = "переранжирование не настроено (kb.rerank_url) — искал одной ступенью"
		}
	}

	deps := find.Deps{Coll: coll, Embedder: m.embedder(), Reranker: rr, Steps: m.steps}
	opts := find.Opts{
		Turn:           m.turnID,
		Mode:           mode,
		TableBoost:     m.cfg.KB.TableBoost,
		Collection:     coll.Name(),
		TopK:           a.TopK,
		MaxPerBook:     liveMaxPerDoc,
		MinCosine:      liveMinCos,
		SemanticWeight: liveSemWeight,
		Semantic:       m.cfg.KB.Semantic,
		Rerank:         a.Rerank,
		Entities:       m.cfg.Mix.Entities,
		Neighbors:      m.cfg.Mix.Neighbors,
		Rank:           m.live.Rank(),
		Full:           a.Full,
		QueryTimeout:   m.cfg.KB.QueryTimeoutDuration(),
		RerankOpts: kb.RerankOpts{
			Candidates: m.cfg.KB.RerankCandidates,
			Snippet:    m.cfg.KB.RerankSnippet,
		},
	}

	m.gen.find++
	gen := m.gen.find
	rules := m.cfg.Graph.Rules()
	cache := m.gr.cache
	dir, chunks := coll.Dir(), coll.ChunkCount()

	m.addBlock(block{kind: blockNotice, text: "ищу по графу и книгам…"})
	return func() tea.Msg {
		// Граф берётся из общего кэша: свой m.gr.open закрывают /kb use
		// и /graph rm, а горутина об этом не узнает.
		if withGraph {
			g, release := openGraphForSearch(cache, dir, chunks, rules)
			if release != nil {
				defer release()
			}
			deps.Graph = g
		}

		ctx, cancel := context.WithTimeout(context.Background(), searchDeadline)
		defer cancel()
		res, err := find.Search(ctx, deps, a.Query, opts)
		return searchDoneMsg{gen: gen, res: res, full: a.Full, err: err, note: note, src: coll}
	}
}

// searchDeadline — общий предел на поиск. Складывается из ожидания вектора
// (kb.query_timeout, по умолчанию 15 с), открытия графа (11.5 с) и самого
// поиска (десятки миллисекунд) с запасом на разросшуюся библиотеку.
const searchDeadline = 2 * time.Minute

// openGraphForSearch открывает граф для фонового поиска.
//
// Отсутствие графа — не ошибка: поиск покажет одни выдержки из книг
// и скажет об этом строкой.
func openGraphForSearch(cache *graph.Cache, dir string, chunks int, rules graph.Rules) (*graph.Graph, func()) {
	if cache != nil {
		g, release, err := cache.Get(dir, chunks)
		if err != nil {
			return nil, nil
		}
		return g, release
	}
	g, err := graph.Open(dir, chunks, rules)
	if err != nil {
		return nil, nil
	}
	return g, func() { _ = g.Close() }
}

// applySearchResult показывает выдачу и запоминает её для /read и Ctrl+F.
func (m *Model) applySearchResult(msg searchDoneMsg) {
	if msg.gen != m.gen.find {
		return // ответ на прошлый вопрос: человек успел задать новый
	}
	if msg.note != "" {
		m.addBlock(block{kind: blockHint, text: msg.note})
	}
	if msg.err != nil {
		m.addBlock(block{kind: blockError, text: msg.err.Error()})
		return
	}
	m.finds = msg.res.Excerpts
	m.findQuery = msg.res.Query
	if m.findPane != nil {
		m.findPane.clampCursor(len(m.finds))
	}
	m.addBlock(block{kind: blockNotice, text: strings.TrimRight(find.Render(msg.res, msg.full, msg.src), "\n")})
}
