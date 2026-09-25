package tools

// search — выдача `/search` целиком: граф и книги одним вызовом.
//
// **Зачем ещё один инструмент, когда есть kb_search и graph_search.**
// Ядро поиска одно с этапа 91 (R2), но слито оно только у человека:
// `find.Search` зовут `/search` в интерфейсе, `--graph-find` и замеры,
// а у модели те же половины лежат порознь — `graph_search` даёт понятия
// и связи (`graph.Search`), `kb_search` — выдержки из книг (`find.Books`).
// Слияние по местам, отбор общего числа мест между графом и книгами и
// перевод вопроса на язык библиотеки именами найденных понятий модель
// в этом случае делает сама или не делает вовсе: у владельца
// `kb.expand_limit = 0`, и связка «понятия графа → имена → запрос к книгам»
// не работает ни в одном инструменте (разбор 24.09.2026).
//
// Этот инструмент закрывает разрыв: он не содержит своей логики поиска —
// вызывает `find.Search` теми же полями, что интерфейс, и печатает тем же
// `find.Render`. То есть модель получает буквально то же, что видит человек.
//
// **Выключатель — список `agent.tools` в конфиге.** Новый инструмент
// не попадает ни в диалог, ни в службу MCP, пока его не впишут туда руками:
// реестр собирается перечислением имён, а `ReadOnlyNames` (поверхность
// службы) намеренно не расширен. Так правку можно померить «до/после»
// одним бинарём, ничего не меняя тем, кто её не просил.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/textx"
)

type searchTool struct{ opts Options }

func (t *searchTool) Name() string { return NameSearch }

func (t *searchTool) Spec() ollama.Tool {
	return ollama.Tool{Type: "function", Function: ollama.ToolSpec{
		Name: NameSearch,
		Description: "Ищет по графу понятий И по книгам разом, одной выдачей: понятия вопроса, " +
			"связи между ними и выдержки из книг со ссылкой на книгу, год и страницу. " +
			"Это то же самое, что видит человек по команде /search. " +
			"Бери его, когда вопрос о связи понятий или когда неизвестно, где искать ответ; " +
			NameKBSearch + " нужен, только когда нужны одни выдержки без графа. " +
			"Чтобы прочитать больше вокруг выдержки, вызови " + NameKBRead + " с её id. " +
			kb.AnswerStyle(t.opts.AnswerStyle),
		Parameters: ollama.ToolParams{
			Type: "object",
			Properties: map[string]ollama.ToolProp{
				"query":      {Type: "string", Description: "Вопрос своими словами или точный термин"},
				"collection": {Type: "string", Description: "Имя коллекции; по умолчанию выбранная пользователем"},
				"book":       {Type: "string", Description: "Брать выдержки только из книг, чьё название, автор или путь содержат эту строку. Карта понятий при этом строится по всей библиотеке: связи между понятиями не принадлежат одной книге"},
				"top_k":      {Type: "integer", Description: "Сколько выдержек вернуть, 1..20"},
				"full":       {Type: "boolean", Description: "Показывать куски целиком, а не выдержками"},
			},
			Required: []string{"query"},
		},
	}}
}

func (t *searchTool) Plan(args map[string]any) (*Plan, error) {
	query, err := requireString(args, "query")
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("пустой запрос")
	}
	name := strings.TrimSpace(argStringOr(args, "collection", ""))
	book := strings.TrimSpace(argStringOr(args, "book", ""))
	topK := argInt(args, "top_k", 0)
	full := argBool(args, "full", false)

	title := fmt.Sprintf("%s(%s)", NameSearch, textx.ShortenOneLine(query, 50))
	if name != "" {
		title = fmt.Sprintf("%s(%s, %s)", NameSearch, name, textx.ShortenOneLine(query, 40))
	}
	return &Plan{
		Tool: NameSearch,
		// Как и у kb_search: читаются собственные данные приложения,
		// поэтому цель проверки прав — каталог базы знаний, а не книги.
		Req:   permissions.Request{Kind: permissions.KindRead, Target: t.opts.KBDir, Tool: NameSearch, Fixed: true},
		Title: title,
		Run: func(ctx context.Context) (string, error) {
			return t.run(ctx, name, query, book, topK, full)
		},
	}, nil
}

func (t *searchTool) run(ctx context.Context, name, query, book string, topK int, full bool) (string, error) {
	// Где искать по книгам: своя коллекция или общая библиотека организации —
	// тем же путём, что kb_search.
	src, err := t.opts.collection(name)
	if err != nil {
		return "", err
	}
	// Местная коллекция нужна всегда: по ней читаются подтверждения графа
	// и собирается сама выдача (find.Render). Там, где библиотека сетевая,
	// коллекции на диске нет — тогда отказ с объяснением, а не половина
	// выдачи под видом целой; для такой раскладки есть kb_search.
	coll, collName, err := localColl(t.opts, name)
	if err != nil {
		return "", err
	}
	// Граф — через общий кэш, как у инструментов графа. Его отсутствие
	// не ошибка: поиск покажет одни выдержки и скажет об этом строкой,
	// ровно как /search (openGraphForSearch в internal/ui).
	var g *graph.Graph
	if _, gg, _, release, gerr := graphOpen(t.opts, name); gerr == nil {
		defer release()
		g = gg
	}

	kbTopK, maxPerDoc, minCos, semWeight := t.opts.kbNumbers()
	if topK > 0 {
		kbTopK = min(topK, 20)
	}
	deps := find.Deps{Coll: coll, Source: src, Graph: g,
		Embedder: t.opts.embedder(), Reranker: t.opts.reranker()}
	if t.opts.Library == nil {
		deps.Source = coll // одна и та же коллекция: и книги, и подтверждения
	}
	o := find.Opts{
		Mode:           "tool",
		Collection:     collName,
		TopK:           kbTopK,
		MaxPerBook:     maxPerDoc,
		TableBoost:     t.opts.KBTableBoost,
		DedupeCosine:   t.opts.KBDedupeCosine,
		Semantic:       t.opts.Semantic,
		MinCosine:      minCos,
		SemanticWeight: semWeight,
		Rerank:         t.opts.reranker() != nil,
		Rank:           t.opts.rank(),
		Full:           full,
		QueryTimeout:   t.opts.QueryTimeout,
		RerankOpts:     t.opts.RerankOpts,
	}
	// Отбор по книге — то же, что у kb_search. Без него слитый поиск не мог бы
	// заменить его собой: «что говорит ВОТ ЭТА книга» — обычный вопрос
	// (25.09.2026, этап 105, К7 — возражение против замены без этого параметра).
	//
	// Отбор действует на ВЫДЕРЖКИ, а не на карту понятий: связь «X использует Y»
	// подтверждена многими книгами сразу и одной книге не принадлежит. Так и
	// написано в описании инструмента, чтобы модель не считала карту
	// отфильтрованной.
	if book != "" {
		o.Docs = booksMatching(coll, book)
	}
	res, err := find.Search(ctx, deps, query, o)
	if err != nil {
		return "", err
	}
	return find.Render(res, full, coll), nil
}
