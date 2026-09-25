package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	gmaint "github.com/Cyber-Watcher/ollchat/internal/graph/maint"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
	"github.com/Cyber-Watcher/ollchat/internal/mcp"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Какие инструменты отдаёт служба.
//
// Список закрыт и проверяется здесь, а не берётся из agent.tools в настройках.
// Причина: в конфиге у ollchat включены bash, write_file и edit_file — они
// нужны агенту в рабочем каталоге, но служба, доступная по сети нескольким
// клиентам, ничего писать и запускать не должна. Читать книги и граф —
// сколько угодно; менять что-либо на машине — нет.
// readOnlyTools — набор службы. Список общий с `ollchat --serve --mcp`:
// двух представлений о том, что безопасно раздавать, быть не должно.
var readOnlyTools = tools.ReadOnlyNames()

// build собирает сервер по настройкам ollchat.
func build(cfg *config.Config, service bool) (*mcp.Server, kbserve.Opts, error) {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return nil, kbserve.Opts{}, fmt.Errorf("база знаний %s: %w", cfg.KB.Dir, err)
	}

	// Песочница нужна инструментам как поле, но ни один из включённых
	// в службу к файлам не ходит: kb_search и kb_read читают готовый индекс.
	sandbox, err := permissions.NewSandbox(cfg.KB.Dir, false, false, cfg.Sandbox.MaxFileKB)
	if err != nil {
		return nil, kbserve.Opts{}, err
	}

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}

	enabled := make([]string, 0, len(readOnlyTools))
	for _, name := range readOnlyTools {
		// web_search без адреса SearXNG не заводим: инструмент, который умеет
		// только объяснить, чего ему не хватает, в списке службы лишний.
		if name == tools.NameWebSearch && strings.TrimSpace(cfg.Web.SearxngURL) == "" {
			continue
		}
		enabled = append(enabled, name)
	}

	// Служба живёт неделями и спрашивает граф часто, поэтому держит его
	// открытым между вызовами: открытие графа коллекции books стоит 11.7 с
	// и гигабайт памяти (замер 28.08.2026). Срок простоя здесь длиннее, чем
	// в диалоге, — служба для того и заведена, чтобы отвечать быстро.
	//
	// Кеш один на всю службу: его берут и инструменты графа, и kb_status.
	// До 11.09.2026 kb_status открывал граф сам на каждый вызов — на рабочей машине
	// это 77 с, и клиент MCP не дожидался ответа.
	//
	// Обновление в фоне (решение владельца 11.09.2026): пока идёт сборка, файлы
	// графа меняются каждые несколько секунд, и кеш со сверкой открывал граф
	// заново на каждый вызов — три kb_status подряд 78/77/77 с. Теперь служба
	// отвечает по уже открытому графу сразу, с пометкой о его времени, а свежий
	// открывает в фоне. Прогрев при старте — чтобы и первый вопрос после
	// перезапуска службы не ждал открытия графа.
	var graphCache *graph.Cache
	if cfg.Graph.Cache {
		// Прогрев и вечное удержание графа — только у службы (--http). Раньше
		// прогрев стоял здесь без условия: `ollmcp --tools` открывал рабочий
		// граф (десятки секунд, до гигабайта), чтобы напечатать список и выйти,
		// а каждый stdio-сеанс клиента держал свой граф вечно, даже если о графе
		// ни разу не спросили (аудит 17.09.2026, Б15).
		ttl := stdioGraphTTL
		if service {
			ttl = serviceGraphTTL
		}
		graphCache = graph.NewCache(ttl, cfg.Graph.Rules()).RefreshInBackground()
		if service {
			go warmGraphs(base, graphCache, cfg.Graph.Rules())
		}
	} // иначе graph.cache = false: открывать на каждый вызов
	summarizer, summaryOpts := gmaint.LazySummarizer(cfg)
	registry, err := tools.NewRegistry(enabled, tools.Options{
		GraphRules:     cfg.Graph.Rules(),
		Summarizer:     summarizer,
		SummaryOpts:    summaryOpts,
		KBTableBoost:   cfg.KB.TableBoost,
		KBExpandLimit:  cfg.KB.ExpandLimitOr(),
		KBDedupeCosine: cfg.KB.DedupeCosine,
		KBAbstainGap:   cfg.KB.AbstainGap,
		KBAbstainScore: cfg.KB.AbstainScore,
		Sandbox:        sandbox,
		MaxOutputKB:    cfg.Agent.MaxOutputKB,
		KB:             base,
		KBDir:          cfg.KB.Dir,
		KBDefault:      cfg.KB.Default,
		GraphCache:     graphCache,
		GraphNeighbors: graph.NeighborRank{
			SenseWeight: cfg.Graph.NeighborSenseWeight,
			Pool:        cfg.Graph.NeighborPool,
		},
		GraphMinRating:       cfg.Graph.MinRating,
		GraphRelationSnippet: cfg.Graph.RelationSnippet,
		Reranker:             kbrerank.New(cfg.KB.RerankOptions()),
		RerankOpts: kb.RerankOpts{
			Candidates: cfg.KB.RerankCandidates,
			Snippet:    cfg.KB.RerankSnippet,
		},
		KBTopK:         cfg.KB.TopK,
		KBMaxPerBook:   cfg.KB.MaxPerBook,
		Semantic:       cfg.KB.Semantic,
		QueryTimeout:   cfg.KB.QueryTimeoutDuration(),
		MinCosine:      cfg.KB.MinCosine,
		SemanticWeight: cfg.KB.SemanticWeight,
		AnswerStyle:    cfg.KB.AnswerStyle,
		SearxURL:       cfg.Web.SearxngURL,
		SearxTimeout:   cfg.Web.TimeoutDuration(),
		Embedder:       kbembed.New(cfg.KB.EmbedOptions(), fallback, 0, nil),
	})
	if err != nil {
		return nil, kbserve.Opts{}, err
	}
	return mcp.NewServer(registry, statusTool(base, cfg.Graph.Rules(), graphCache)),
		kbserve.Opts{
			TableBoost: cfg.KB.TableBoost,
			Reranker:   kbrerank.New(cfg.KB.RerankOptions()),
			RerankOpts: kb.RerankOpts{Candidates: cfg.KB.RerankCandidates, Snippet: cfg.KB.RerankSnippet},
			Base:       base,
			Emb:        kbembed.New(cfg.KB.EmbedOptions(), fallback, 0, nil),
			Default:    cfg.KB.Default,
			Token:      kbserve.Token(),
		}, nil
}

// serviceGraphTTL — сколько служба держит граф открытым без обращений.
//
// Ноль — не закрывать вовсе (решение владельца 12.09.2026: «граф в службе ollmcp
// должен быть тёплым всегда»). Прежний час стоил ровно того, ради чего служба
// и заведена: после часа тишины граф закрывался, и первый же вопрос ждал его
// открытия — замер 12.09 на рабочей машине: 35.6 с холодным против 0.016 с тёплым,
// и клиент MCP столько не ждёт. Спрашивают службу двое (ассистент и владелец),
// перерывы между пачками вопросов бывают в часы, так что закрытие по простою
// било почти по каждой первой просьбе.
//
// Цена — гигабайт с небольшим постоянно занятой памяти на графе books (замер
// 28.08.2026: 160 МБ на сам граф, около 1 ГБ пик процесса при открытии) плюс
// второй экземпляр на время фоновой подмены. Это и есть плата за службу,
// которая отвечает сразу; TUI ollchat закрывает свой граф по-прежнему.
const serviceGraphTTL = 0

// stdioGraphTTL — сколько держит граф сеанс stdio: его запускает клиент MCP
// на время разговора, и граф, о котором перестали спрашивать, память не занимает.
const stdioGraphTTL = 15 * time.Minute

// statusTool — справка о том, на что клиент вообще может опираться.
//
// Без неё клиент не отличает «в книгах об этом не написано» от «книги по этой
// теме не проиндексированы», а это разные ответы. Заодно видно, посчитаны ли
// смыслы и разобран ли граф: и то и другое меняет качество поиска.
//
// Граф берётся из общего кеша службы (cache; nil — открыть на вызов): см. statusGraph.
func statusTool(base *kb.Base, rules graph.Rules, cache *graph.Cache) mcp.Tool {
	return mcp.Tool{
		Spec: ollama.ToolSpec{
			Name: "kb_status",
			Description: "Показывает состав личной библиотеки: коллекции, сколько в них книг " +
				"и фрагментов, посчитаны ли векторы(смыслы), собран ли граф понятий и сколько " +
				"в нём сущностей и связей. Позови, чтобы понять, на что можно опираться.",
			Parameters: ollama.ToolParams{Type: "object", Properties: map[string]ollama.ToolProp{}},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			names, err := base.Names()
			if err != nil {
				return "", err
			}
			if len(names) == 0 {
				return "База знаний пуста: коллекций нет.", nil
			}
			sort.Strings(names)
			var b strings.Builder
			for _, n := range names {
				coll, err := base.Open(n)
				if err != nil {
					fmt.Fprintf(&b, "%s: не открывается (%v)\n", n, err)
					continue
				}
				st := coll.Stats()
				fmt.Fprintf(&b, "коллекция %s: книг %d, фрагментов %d", n, st.Indexed, st.Chunks)
				if st.Vectors > 0 {
					percent := 100 * st.Vectors / max(st.Chunks, 1)
					fmt.Fprintf(&b, ", векторы(смыслы) %d%% (%s)", percent, st.VecModel)
				} else {
					b.WriteString(", векторов(смыслов) нет — поиск идёт по словам")
				}
				b.WriteString("\n")

				g, release, err := statusGraph(cache, coll.Dir(), coll.ChunkCount(), rules)
				if err != nil {
					fmt.Fprintf(&b, "  граф понятий: %v\n", err)
					continue
				}
				gs := g.Stats(coll.ChunkCount())
				opened, refreshing := g.Freshness()
				release()
				fmt.Fprintf(&b, "  граф понятий: сущностей %d, связей %d, разобрано фрагментов %d из %d\n",
					gs.Entities, gs.Edges, gs.Covered, coll.ChunkCount())
				if refreshing {
					fmt.Fprintf(&b, "  (числа графа на %s: сборка с тех пор дописала файлы, свежий граф "+
						"открывается в фоне — следующий вызов получит его)\n", opened.Format("15:04:05"))
				}
			}
			return b.String(), nil
		},
	}
}

// warmGraphs открывает в фоне графы всех коллекций, у которых граф есть.
//
// Без прогрева первый вопрос после запуска службы ждал бы открытия графа —
// на рабочей машине 77 с, дольше, чем ждёт клиент MCP, — а перезапускают службу
// сторож и ассистент (privatescripts/bin/ollmcp-keeper.sh), то есть не редко.
func warmGraphs(base *kb.Base, cache *graph.Cache, rules graph.Rules) {
	names, err := base.Names()
	if err != nil {
		return
	}
	for _, n := range names {
		coll, err := base.Open(n)
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(coll.Dir(), graph.DirFor(rules.Name))); err != nil {
			continue // графа у коллекции нет — греть нечего
		}
		cache.Warm(coll.Dir(), coll.ChunkCount())
	}
}

// statusGraph отдаёт граф коллекции для kb_status и возврат, который вызывать
// обязательно.
//
// С кешем — тот же экземпляр, что у инструментов графа. Актуальность сверяет
// сам кеш: на каждое обращение он снимает отпечаток файлов каталога графа
// (имена, размеры, времена правки) и, если сборка что-то дописала, открывает
// граф заново, а прежний закрывает, когда его отпустят. Сборке это не мешает:
// замок сборки берёт и снимает только тот, кто его взял (Graph.Unlock при
// g.lock == nil ничего не делает), а в файлы графа служба ничего не дописывает.
//
// Без кеша (graph.cache = false) — открыть и закрыть, как до 11.09.2026.
func statusGraph(cache *graph.Cache, collDir string, chunks int, rules graph.Rules) (*graph.Graph, func(), error) {
	if cache != nil {
		return cache.Get(collDir, chunks)
	}
	g, err := graph.Open(collDir, chunks, rules)
	if err != nil {
		return nil, nil, err
	}
	return g, func() { g.Close() }, nil
}
