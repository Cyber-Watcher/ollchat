package tools

import (
	"context"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/textx"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// Поиск по графу понятий.
//
// Отличие от kb_search стоит проговорить прямо, потому что модель сама его
// не угадает: kb_search отвечает на «где про это написано», а эти три —
// на «как это связано с тем». Поэтому в описаниях инструментов сказано,
// когда звать какой: описания читает модель, и они решают, будет ли граф
// использован вообще.
//
// Собирать граф модель не может: инструмента для этого нет и не будет.
// Сборка занимает видеокарту на часы, и запускает её человек командой
// ollchat --graph-build.

// graphOpen открывает граф коллекции. Общая часть всех инструментов графа.
//
// Возвращает **возврат, а не сам граф на закрытие**: когда задан `GraphCache`,
// граф общий и закрывать его вызывающему нельзя — им пользуются другие. Без
// кэша возврат просто закрывает граф, как было раньше.
//
// Открытие графа коллекции books стоит 11.7 с и гигабайт памяти (замер
// 28.08.2026), поэтому в службе `ollmcp` кэш обязателен: без него каждый
// вопрос ассистента платил эти секунды заново.
//
// **Граф читается из локальных файлов даже тогда, когда книги идут по сети.**
// Так вышло не по замыслу, а потому, что раздача графа службой ещё не сделана;
// см. graphOverNetwork ниже — отказ обязан объяснять это прямо.
func graphOpen(opts Options, collection string) (*kb.Collection, *graph.Graph, string, func(), error) {
	if opts.KB == nil {
		return nil, nil, "", nil, fmt.Errorf("база знаний не настроена")
	}
	name := strings.TrimSpace(collection)
	if name == "" {
		name = opts.KBDefault
	}
	if name == "" {
		names, err := opts.KB.Names()
		if err != nil {
			return nil, nil, "", nil, err
		}
		if len(names) == 0 {
			return nil, nil, "", nil, fmt.Errorf("в базе знаний нет коллекций")
		}
		name = names[0]
	}
	coll, err := opts.KB.Open(name)
	if err != nil {
		return nil, nil, "", nil, graphOverNetwork(opts, name, err)
	}
	if opts.GraphCache != nil {
		g, release, err := opts.GraphCache.Get(coll.Dir(), coll.ChunkCount())
		if err != nil {
			return nil, nil, "", nil, fmt.Errorf("граф коллекции %s недоступен: %w", name, err)
		}
		return coll, g, name, release, nil
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), opts.GraphRules)
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("граф коллекции %s недоступен: %w", name, err)
	}
	return coll, g, name, func() { g.Close() }, nil
}

// rank — ранжирование связей: живое значение главнее записанного в конфиге.
func (o Options) rank() graph.NeighborRank {
	if o.Live != nil {
		return o.Live.Rank()
	}
	return o.GraphNeighbors
}

// kbNumbers — числа поиска по книгам с той же оговоркой.
func (o Options) kbNumbers() (topK, maxPerDoc int, minCos, semWeight float64) {
	if o.Live != nil {
		return o.Live.KB()
	}
	return o.KBTopK, o.KBMaxPerBook, o.MinCosine, o.SemanticWeight
}

// graphOverNetwork объясняет отказ графа, когда книги берутся из общей библиотеки.
//
// **Зачем отдельное объяснение.** Без него человек с настроенным `kb.server_url`
// получал бы «коллекции "books" нет» — и шёл искать беду в книгах, хотя книги
// прекрасно находятся, а нет именно графа: инструменты графа читают локальные
// файлы, потому что раздача графа службой ещё не сделана. Ложный след дороже
// самого отказа: он уводит от причины.
//
// Сообщение говорит три вещи сразу: что именно не работает, почему, и что
// делать сейчас.
func graphOverNetwork(opts Options, name string, err error) error {
	if opts.Library == nil {
		return err // обычная работа с файлами — отказ и так по существу
	}
	return fmt.Errorf("граф понятий через общую библиотеку пока не раздаётся: "+
		"поиск по книгам идёт к службе, а граф читается из локальных файлов, "+
		"и коллекции %q среди них нет.\n"+
		"Сейчас: спрашивайте книги — kb_search работает. "+
		"Чтобы заработал и граф, нужна локальная копия коллекции с графом "+
		"в каталоге kb.dir (перенос описан в README, раздел о переносимости)", name)
}

// queryVector — вектор вопроса под векторы графа, тем же ядром, что
// у /search (этап 91, R2.5). Не дождались или модели не совпали — пустой
// вектор: вход в граф остаётся словесным, и это законное вырождение.
func queryVector(ctx context.Context, opts Options, g *graph.Graph, query string) []int8 {
	qv, _, _ := find.QueryVector(ctx, find.Deps{Graph: g, Embedder: opts.embedder()}, query,
		find.Opts{Semantic: true, QueryTimeout: opts.queryTimeout()})
	return qv
}

// queryTimeout — сколько ждать вектор вопроса: свой короткий срок, а не общий
// на пакетную векторизацию (замер 30.08.2026: наследованный kb.embed_timeout
// в пятнадцать минут вешал вопрос к графу под идущей сборкой).
func (o Options) queryTimeout() time.Duration {
	if o.QueryTimeout > 0 {
		return o.QueryTimeout
	}
	return kb.DefaultQueryTimeout
}

// graphNote приписывает к ответу честную оговорку о неполноте графа.
//
// Без неё «ничего не нашлось» читается как «в книгах об этом не пишут», а это
// разные утверждения: граф собирается каталогами и покрывает пока не всю
// библиотеку.
func graphNote(g *graph.Graph, coll *kb.Collection) string {
	st := g.Stats(coll.ChunkCount())
	if st.Pending <= 0 || coll.ChunkCount() == 0 {
		return ""
	}
	percent := 100 * st.Covered / coll.ChunkCount()
	return fmt.Sprintf("\nГраф собран по %d%% библиотеки (%d фрагментов из %d): "+
		"если понятия здесь нет, оно может быть в неразобранных книгах — поищи kb_search.",
		percent, st.Covered, coll.ChunkCount())
}

// graphReq — что проверять правилами доступа. Граф лежит внутри каталога базы
// знаний и наружу не ходит, поэтому проверка та же, что у kb_search.
func graphReq(opts Options, tool string) permissions.Request {
	return permissions.Request{Kind: permissions.KindRead, Target: opts.KBDir, Tool: tool, Fixed: true}
}

// ── graph_search ─────────────────────────────────────────────────────────────

type graphSearchTool struct{ opts Options }

func (t *graphSearchTool) Name() string { return NameGraphSearch }

func (t *graphSearchTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameGraphSearch,
		Description: "Ищет по графу понятий, построенному из книг пользователя. " +
			"Отвечает на вопросы о СВЯЗЯХ: как соотносятся два понятия, что с чем связано, " +
			"из чего состоит тема. Возвращает понятия, связи между ними и цитаты из книг " +
			"с указанием книги и страницы. " +
			"Когда нужен просто текст по теме — быстрее " + NameKBSearch + ". " +
			"Когда нужна карточка одного понятия — " + NameGraphEntity + ", " +
			"когда нужна цепочка между двумя — " + NameGraphPath + ". " +
			"Ссылайся на книги и страницы из выдачи, называя книгу по названию, " +
			"а не по фамилии автора: ответ по книгам без ссылок " +
			"ничем не отличается от выдумки.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"query":      {Type: "string", Description: "Вопрос своими словами или перечень понятий"},
				"collection": {Type: "string", Description: "Имя коллекции; по умолчанию выбранная пользователем"},
				"top_k":      {Type: "integer", Description: "Сколько понятий взять за основу, 1..12"},
			},
			Required: []string{"query"},
		},
	}}
}

func (t *graphSearchTool) Plan(args map[string]any) (*Plan, error) {
	query, err := requireString(args, "query")
	if err != nil {
		return nil, err
	}
	collection := strings.TrimSpace(argStringOr(args, "collection", ""))
	topK := argInt(args, "top_k", 0)

	title := fmt.Sprintf("%s(%s)", NameGraphSearch, textx.ShortenOneLine(query, 40))
	return &Plan{
		Tool: NameGraphSearch, Req: graphReq(t.opts, NameGraphSearch), Title: title,
		Run: func(ctx context.Context) (string, error) {
			coll, g, name, release, err := graphOpen(t.opts, collection)
			if err != nil {
				return "", err
			}
			defer release()

			res := g.Search(query, graph.SearchOpts{
				TopEntities: topK,
				// Подтверждения отбираются по словам вопроса, а не только
				// по густоте понятий: иначе выигрывают отзывы на обложке.
				Rank: graph.RankWith(coll),
				// Смысловой вход: вопрос на русском находит понятие,
				// записанное по-английски, и глагол находит существительное.
				QueryVector: queryVector(ctx, t.opts, g, query),
				Neighbors:   t.opts.rank(),
			})
			out := graph.Render(coll, res, graph.RenderOpts{ForModel: true, Collection: name, RelationRunes: t.opts.GraphRelationSnippet})
			return out + graphNote(g, coll), nil
		},
	}, nil
}

// ── graph_entity ─────────────────────────────────────────────────────────────

type graphEntityTool struct{ opts Options }

func (t *graphEntityTool) Name() string { return NameGraphEntity }

func (t *graphEntityTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameGraphEntity,
		Description: "Карточка одного понятия из графа книг: другие его написания, " +
			"в скольких книгах встречается, с чем связано и где об этом написано. " +
			"Зови, когда нужно разобраться в одном термине, а не в связи между двумя.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"name":       {Type: "string", Description: "Название понятия, как оно встречается в книгах"},
				"collection": {Type: "string", Description: "Имя коллекции; по умолчанию выбранная пользователем"},
			},
			Required: []string{"name"},
		},
	}}
}

func (t *graphEntityTool) Plan(args map[string]any) (*Plan, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}
	collection := strings.TrimSpace(argStringOr(args, "collection", ""))

	title := fmt.Sprintf("%s(%s)", NameGraphEntity, textx.ShortenOneLine(name, 40))
	return &Plan{
		Tool: NameGraphEntity, Req: graphReq(t.opts, NameGraphEntity), Title: title,
		Run: func(ctx context.Context) (string, error) {
			coll, g, collName, release, err := graphOpen(t.opts, collection)
			if err != nil {
				return "", err
			}
			defer release()

			ent, ok := g.Entity(name, graph.SearchOpts{})
			if !ok {
				return fmt.Sprintf("понятия «%s» в графе нет.%s", name, graphNote(g, coll)), nil
			}
			chunks := g.Search(ent.Name, graph.SearchOpts{
				TopEntities: 1, TopChunks: 4, Rank: graph.RankWith(coll),
			}).Chunks
			out := graph.RenderEntity(coll, ent, chunks, graph.RenderOpts{Collection: collName, RelationRunes: t.opts.GraphRelationSnippet})
			return out + graphNote(g, coll), nil
		},
	}, nil
}

// ── graph_path ───────────────────────────────────────────────────────────────

type graphPathTool struct{ opts Options }

func (t *graphPathTool) Name() string { return NameGraphPath }

func (t *graphPathTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameGraphPath,
		Description: "Показывает цепочку связей между двумя понятиями из книг: " +
			"через что одно связано с другим, с подтверждением на каждом шаге. " +
			"Зови на вопросы вида «как связаны X и Y», когда прямой связи может и не быть.",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"from":       {Type: "string", Description: "Первое понятие"},
				"to":         {Type: "string", Description: "Второе понятие"},
				"max_hops":   {Type: "integer", Description: "Сколько шагов допустимо, 1..6; по умолчанию 4"},
				"collection": {Type: "string", Description: "Имя коллекции; по умолчанию выбранная пользователем"},
			},
			Required: []string{"from", "to"},
		},
	}}
}

func (t *graphPathTool) Plan(args map[string]any) (*Plan, error) {
	from, err := requireString(args, "from")
	if err != nil {
		return nil, err
	}
	to, err := requireString(args, "to")
	if err != nil {
		return nil, err
	}
	collection := strings.TrimSpace(argStringOr(args, "collection", ""))
	hops := argInt(args, "max_hops", 4)

	title := fmt.Sprintf("%s(%s → %s)", NameGraphPath, textx.ShortenOneLine(from, 24), textx.ShortenOneLine(to, 24))
	return &Plan{
		Tool: NameGraphPath, Req: graphReq(t.opts, NameGraphPath), Title: title,
		Run: func(ctx context.Context) (string, error) {
			coll, g, name, release, err := graphOpen(t.opts, collection)
			if err != nil {
				return "", err
			}
			defer release()

			steps, ok := g.Path(from, to, hops)
			out := graph.RenderPath(coll, from, to, steps, ok, graph.RenderOpts{Collection: name, RelationRunes: t.opts.GraphRelationSnippet})
			return out + graphNote(g, coll), nil
		},
	}, nil
}

// ── graph_overview ───────────────────────────────────────────────────────────

type graphOverviewTool struct{ opts Options }

func (t *graphOverviewTool) Name() string { return NameGraphOverview }

func (t *graphOverviewTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameGraphOverview,
		Description: "Даёт обзор темы по книгам пользователя: из каких подтем она состоит, " +
			"что в каждую входит и в каких книгах разобрана. " +
			"Зови на широкие вопросы — «что вообще есть про X», «с чего начать изучать X», " +
			"«из чего состоит X». " +
			"Когда нужна связь двух понятий — " + NameGraphSearch + ", " +
			"когда нужны сами слова из книги — " + NameKBSearch + ". " +
			"Описания тем составлены по графу, а не выписаны из книг: " +
			"выдавать их за цитаты нельзя, за цитатами иди в " + NameKBSearch + ".",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"query":      {Type: "string", Description: "Тема или широкий вопрос своими словами"},
				"collection": {Type: "string", Description: "Имя коллекции; по умолчанию выбранная пользователем"},
				"top_k":      {Type: "integer", Description: "Сколько тем показать, 1..10"},
			},
			Required: []string{"query"},
		},
	}}
}

func (t *graphOverviewTool) Plan(args map[string]any) (*Plan, error) {
	query, err := requireString(args, "query")
	if err != nil {
		return nil, err
	}
	collection := strings.TrimSpace(argStringOr(args, "collection", ""))
	topK := argInt(args, "top_k", 0)

	title := fmt.Sprintf("%s(%s)", NameGraphOverview, textx.ShortenOneLine(query, 40))
	return &Plan{
		Tool: NameGraphOverview, Req: graphReq(t.opts, NameGraphOverview), Title: title,
		Run: func(ctx context.Context) (string, error) {
			coll, g, _, release, err := graphOpen(t.opts, collection)
			if err != nil {
				return "", err
			}
			defer release()

			comms, err := g.LoadCommunities()
			if err != nil {
				return "", err
			}
			res := g.Overview(query, comms, graph.OverviewOpts{
				TopCommunities: topK,
				QueryVector:    queryVector(ctx, t.opts, g, query),
				MinRating:      t.opts.GraphMinRating,
			})
			out := graph.RenderOverview(res, bookName(coll))
			return out + graphNote(g, coll), nil
		},
	}, nil
}

// bookName переводит номер книги в её название: в резюме сообществ книги
// записаны номерами, а человеку и модели нужны названия.
func bookName(coll *kb.Collection) func(string) string {
	return func(id string) string {
		n, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			return ""
		}
		for _, b := range coll.Books() {
			if b.ID == uint32(n) {
				if b.Title != "" {
					return b.Title
				}
				return filepath.Base(b.Path)
			}
		}
		return ""
	}
}

// graphTopicTool отдаёт одну тему целиком, вместе с разбором.
//
// Отдельный инструмент, а не поле в обзоре. Обзор из пяти тем укладывается
// в 1.0–1.5 тыс. токенов, а с разбором занял бы 7–15 тыс. — половину окна,
// которое нужно самим книгам и ответу. Поэтому обзор остаётся картой тем
// и указывает номер, а подробности берутся отдельным вызовом, когда нужны.
type graphTopicTool struct{ opts Options }

func (t *graphTopicTool) Name() string { return NameGraphTopic }

func (t *graphTopicTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameGraphTopic,
		Description: "Показывает одну тему графа целиком: описание, важность, ключевые понятия " +
			"и разбор — что именно про неё известно по книгам. " +
			"Зови после " + NameGraphOverview + ", когда нужна не карта тем, а подробности " +
			"по одной из них: номер темы виден в обзоре. " +
			"Разбор составлен по графу, а не выписан из книг: за цитатами иди в " + NameKBSearch + ".",
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"topic":      {Type: "string", Description: "Номер темы из обзора («1951») или её название"},
				"collection": {Type: "string", Description: "Имя коллекции; по умолчанию выбранная пользователем"},
			},
			Required: []string{"topic"},
		},
	}}
}

func (t *graphTopicTool) Plan(args map[string]any) (*Plan, error) {
	topic, err := requireString(args, "topic")
	if err != nil {
		return nil, err
	}
	collection := strings.TrimSpace(argStringOr(args, "collection", ""))

	title := fmt.Sprintf("%s(%s)", NameGraphTopic, textx.ShortenOneLine(topic, 40))
	return &Plan{
		Tool: NameGraphTopic, Req: graphReq(t.opts, NameGraphTopic), Title: title,
		Run: func(ctx context.Context) (string, error) {
			coll, g, _, release, err := graphOpen(t.opts, collection)
			if err != nil {
				return "", err
			}
			defer release()

			comms, err := g.LoadCommunities()
			if err != nil {
				return "", err
			}
			if comms == nil {
				return "", fmt.Errorf("сообщества не размечены: ollchat --graph-communities")
			}
			com, ok := comms.Topic(topic)
			if !ok {
				return fmt.Sprintf("темы %q в графе нет — посмотрите номера в %s",
					topic, NameGraphOverview), nil
			}
			return graph.RenderTopic(com, bookName(coll)) + graphNote(g, coll), nil
		},
	}, nil
}
