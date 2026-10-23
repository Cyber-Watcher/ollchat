package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbremote"
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

	// text — выдача, уже напечатанная службой (find.Render по её файлам:
	// выдержки под связями и годы связей клиенту взять неоткуда); пусто —
	// печатать здесь по res и src.
	text string
}

// searchCmd запускает поиск по графу и книгам.
func (m *Model) searchCmd(arg string) tea.Cmd {
	return m.searchCmdFor(arg, "", true, "search", "/search")
}

// searchCmdFor — общий ход /search и /kb search: одно ядро, один вид выдачи
// (этап 91, R2.7). collName — коллекция, пусто — выбранная; withGraph —
// входить ли в граф; mode — метка для журнала шагов; where — имя команды
// для ошибок.
//
// С общей библиотекой (kb.server_url) поиск идёт в службу, как у модели
// и подмешивания (решение владельца 23.10.2026: «Команды в службу»): своя
// коллекция и свой граф не открываются (remoteSearchCmd). В положении local
// (/kb source) — свои файлы, граф из общего кэша, как без службы.
func (m *Model) searchCmdFor(arg, collName string, withGraph bool, mode, where string) tea.Cmd {
	liveTopK, _, _, _ := m.live.KB()
	a, err := parseSearchArgs(arg, liveTopK)
	if err != nil {
		m.fail(where, err)
		return nil
	}
	if m.viaService() {
		return m.remoteSearchCmd(a, collName, withGraph, mode)
	}
	coll, err := m.kbCollection(collName)
	if err != nil {
		m.fail(where, err)
		return nil
	}
	p := m.planSearch(a, mode, withGraph)
	m.addBlock(block{kind: blockNotice, text: "ищу по графу и книгам…"})
	return func() tea.Msg { return p.runLocal(coll) }
}

// searchPlan — всё, что поиску нужно от модели; снимается в цикле событий,
// считается в горутине.
type searchPlan struct {
	a         searchArgs
	deps      find.Deps // без коллекции и графа: их даёт ход поиска
	opts      find.Opts // без имени коллекции
	note      string    // оговорка своего поиска: реранкер не настроен
	gen       int
	withGraph bool
	rules     graph.Rules
	cache     *graph.Cache
}

// planSearch снимает с модели числа и настройки поиска и заводит поколение
// выдачи.
func (m *Model) planSearch(a searchArgs, mode string, withGraph bool) searchPlan {
	_, liveMaxPerDoc, liveMinCos, liveSemWeight := m.live.KB()

	// Реранкер: молчать о том, что ключ не сработал, нельзя — человек решит,
	// что вторая ступень отработала, и сравнит несравнимое.
	var rr kb.Reranker
	note := ""
	rerank := a.Rerank
	if rerank {
		if r := kbrerank.New(m.cfg.KB.RerankOptions()); r != nil {
			rr = r
		} else {
			rerank = false
			note = "переранжирование не настроено (kb.rerank_url) — искал одной ступенью"
		}
	}

	m.gen.find++
	return searchPlan{
		a:    a,
		note: note,
		gen:  m.gen.find,
		deps: find.Deps{Embedder: m.embedder(), Reranker: rr, Steps: m.steps,
			Translator: m.translator()},
		opts: find.Opts{
			Turn:           m.turnID,
			Mode:           mode,
			TableBoost:     m.cfg.KB.TableBoost,
			DedupeCosine:   m.cfg.KB.DedupeCosine,
			TopK:           a.TopK,
			MaxPerBook:     liveMaxPerDoc,
			MinCosine:      liveMinCos,
			SemanticWeight: liveSemWeight,
			Semantic:       m.cfg.KB.Semantic,
			Rerank:         rerank,
			Entities:       m.cfg.Mix.Entities,
			Neighbors:      m.cfg.Mix.Neighbors,
			Rank:           m.live.Rank(),
			Full:           a.Full,
			QueryTimeout:   m.cfg.KB.QueryTimeoutDuration(),
			RerankOpts: kb.RerankOpts{
				Candidates: m.cfg.KB.RerankCandidates,
				Snippet:    m.cfg.KB.RerankSnippet,
			},
		},
		withGraph: withGraph,
		rules:     m.cfg.Graph.Rules(),
		cache:     m.gr.cache,
	}
}

// runLocal ищет по своей коллекции coll. Вне цикла событий.
func (p searchPlan) runLocal(coll *kb.Collection) searchDoneMsg {
	deps, opts := p.deps, p.opts
	deps.Coll, opts.Collection = coll, coll.Name()
	// Граф берётся из общего кэша: свой m.gr.open закрывают /kb use
	// и /graph rm, а горутина об этом не узнает.
	if p.withGraph {
		g, release := openGraphForSearch(p.cache, coll.Dir(), coll.ChunkCount(), p.rules)
		if release != nil {
			defer release()
		}
		deps.Graph = g
	}
	ctx, cancel := context.WithTimeout(context.Background(), searchDeadline)
	defer cancel()
	res, err := find.Search(ctx, deps, p.a.Query, opts)
	return searchDoneMsg{gen: p.gen, res: res, full: p.a.Full, err: err, note: p.note, src: coll}
}

// libraryFinder — служба, которая ищет /search у себя (kbremote.Client.Find,
// вход /api/v1/find).
type libraryFinder interface {
	Find(ctx context.Context, req kbremote.FindRequest) (kbremote.FindReply, error)
}

// remoteSearchCmd — /search и /kb search через службу общей библиотеки.
//
// Служба ищет тем же find.Search по своим файлам и печатает тем же
// find.Render: человек видит ту же выдачу, что при библиотеке у себя, и ту
// же, что модель в инструменте search. Числа отбора — живые числа клиента
// (/kb tune). Служба не ответила (kb.ErrUnreachable), а свои файлы есть —
// тот же поиск по ним с пометкой, как у модели (kb.Fallback); занята
// (kb.ErrBusy) или отказала по смыслу — ошибка как есть; в положении service
// (/kb source) и недоступность — ошибка с пометкой. Служба прежней
// сборки входа /api/v1/find не знает — выдержки её поиском по книгам, без
// карты, и строка об этом.
func (m *Model) remoteSearchCmd(a searchArgs, collName string, withGraph bool, mode string) tea.Cmd {
	p := m.planSearch(a, mode, withGraph)
	// Службе — имя, выбранное человеком; своя единственная, выбранная сама,
	// службе не уходит: у неё такой может не быть (как planRemote).
	name := strings.TrimSpace(collName)
	if name == "" && !m.kb.picked {
		name = m.kb.use
	}
	own := strings.TrimSpace(collName)
	if own == "" {
		own = m.kb.use
	}
	req := kbremote.FindRequest{
		Collection: name, Query: a.Query, TopK: a.TopK,
		MaxPerBook: p.opts.MaxPerBook, MinCosine: p.opts.MinCosine, SemanticWeight: p.opts.SemanticWeight,
		Entities: p.opts.Entities, Neighbors: p.opts.Neighbors,
		Graph: withGraph, Rerank: a.Rerank, Full: a.Full,
	}
	finder, _ := m.kb.lib.(libraryFinder)
	lib := m.library()
	base, def := m.kb.base, m.cfg.KB.Default
	serviceOnly := m.kbMode() == kb.SourceService
	if serviceOnly {
		base = nil // положение service: на свои файлы не переходим (/kb source)
	}

	m.addBlock(block{kind: blockNotice, text: "ищу по графу и книгам у службы…"})
	return func() tea.Msg {
		var err error
		if finder != nil {
			ctx, cancel := context.WithTimeout(context.Background(), searchDeadline)
			rep, ferr := finder.Find(ctx, req)
			cancel()
			if ferr == nil {
				return searchDoneMsg{gen: p.gen, res: rep.Result, full: a.Full, text: rep.Text,
					note: joinNote(rep.Note, kb.QueueNote(rep.Queued))}
			}
			err = ferr
		}
		switch {
		case finder == nil || errors.Is(err, kbremote.ErrNotServed):
			return p.runService(lib, name)
		case errors.Is(err, kb.ErrUnreachable) && base != nil:
			coll, lerr := localCollection(base, own, def)
			if lerr != nil {
				return searchDoneMsg{gen: p.gen, err: fmt.Errorf("%w; свои файлы: %v", err, lerr)}
			}
			msg := p.runLocal(coll)
			msg.note = joinNote(kb.FallbackNote, msg.note)
			return msg
		case errors.Is(err, kb.ErrUnreachable) && serviceOnly:
			err = fmt.Errorf("%w (%s)", err, kb.ServiceOnlyNote)
		}
		return searchDoneMsg{gen: p.gen, err: err}
	}
}

// runService — выдержки поиском службы по книгам (вход /api/v1/search), без
// карты понятий: служба прежней сборки поиска с графом не знает.
func (p searchPlan) runService(lib kb.Library, name string) searchDoneMsg {
	src, err := lib.Source(name)
	if err != nil {
		return searchDoneMsg{gen: p.gen, err: err}
	}
	deps, opts := p.deps, p.opts
	deps.Source, opts.Collection = src, src.Name()
	ctx, cancel := context.WithTimeout(context.Background(), searchDeadline)
	defer cancel()
	res, err := find.Search(ctx, deps, p.a.Query, opts)
	note := "служба прежней сборки не ищет по графу для /search — показаны выдержки из книг"
	if !p.withGraph {
		note = ""
	}
	if kb.Fell(src) {
		note = kb.FallbackNote
	}
	chunks, _ := src.(graph.Chunks) // источник Fallback умеет; служба кусков не отдаёт
	return searchDoneMsg{gen: p.gen, res: res, full: p.a.Full, err: err,
		note: joinNote(note, kb.QueueNote(kb.Queued(src)), p.note), src: chunks}
}

// joinNote склеивает непустые оговорки в одну строку.
func joinNote(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "; ")
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
	text := msg.text
	if text == "" {
		text = find.Render(msg.res, msg.full, msg.src)
	}
	m.addBlock(block{kind: blockNotice, text: strings.TrimRight(text, "\n")})
}
