package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
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
func build(cfg *config.Config) (*mcp.Server, kbserve.Opts, error) {
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
	registry, err := tools.NewRegistry(enabled, tools.Options{
		GraphRules:     cfg.Graph.Rules(),
		KBTableBoost:   cfg.KB.TableBoost,
		KBAbstainGap:   cfg.KB.AbstainGap,
		KBAbstainScore: cfg.KB.AbstainScore,
		Sandbox:        sandbox,
		MaxOutputKB:    cfg.Agent.MaxOutputKB,
		KB:             base,
		KBDir:          cfg.KB.Dir,
		KBDefault:      cfg.KB.Default,
		GraphCache: func() *graph.Cache {
			if !cfg.Graph.Cache {
				return nil // graph.cache = false: открывать на каждый вызов
			}
			return graph.NewCache(serviceGraphTTL, cfg.Graph.Rules())
		}(),
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
	return mcp.NewServer(registry, statusTool(base, cfg.Graph.Rules())),
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

// statusTool — справка о том, на что клиент вообще может опираться.
//
// Без неё клиент не отличает «в книгах об этом не написано» от «книги по этой
// теме не проиндексированы», а это разные ответы. Заодно видно, посчитаны ли
// смыслы и разобран ли граф: и то и другое меняет качество поиска.
// serviceGraphTTL — сколько служба держит граф открытым без обращений.
//
// Час: у ассистента вопросы идут пачками с перерывами на чтение и правку кода,
// и закрывать граф между ними значило бы платить одиннадцать секунд на каждой
// пачке. Гигабайт памяти на этот час — цена, ради которой служба и заведена.
const serviceGraphTTL = time.Hour

func statusTool(base *kb.Base, rules graph.Rules) mcp.Tool {
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

				g, err := graph.Open(coll.Dir(), coll.ChunkCount(), rules)
				if err != nil {
					fmt.Fprintf(&b, "  граф понятий: %v\n", err)
					continue
				}
				gs := g.Stats(coll.ChunkCount())
				fmt.Fprintf(&b, "  граф понятий: сущностей %d, связей %d, разобрано фрагментов %d из %d\n",
					gs.Entities, gs.Edges, gs.Covered, coll.ChunkCount())
				g.Close()
			}
			return b.String(), nil
		},
	}
}
