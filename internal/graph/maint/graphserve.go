package maint

// Графовый вход службы знаний: `/api/v1/graph/tool` и `/api/v1/graph/search`.
//
// **Почему здесь, а не в cmd/ollchat/serve.go, где он жил до 22.10.2026.**
// Службу поднимают двое — `ollchat --serve` и `ollmcp --http`, — а реализация
// графового входа лежала в пакете main первой программы, и второй взять её
// было неоткуда: `ollmcp --http` графа клиентам ollchat не раздавал вовсе
// (kbserve.Opts без Graph — оба входа отвечали 501 «граф не подключён»).
// Это ровно то, ради чего этап 121: клиент с kb.server_url должен брать карту
// понятий у службы, которая граф уже держит, а не открывать свой. Писать
// в ollmcp вторую реализацию значило бы две выдачи на один вопрос; поэтому
// одна реализация перенесена сюда, к QueryVector и NeighborRank, которыми она
// и считает, и обе программы зовут её.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// GraphService отвечает на вопросы к графу от имени службы.
//
// Инструменты берутся из **того же реестра**, что у локального ollchat: клиент
// обязан получить ровно тот текст, который увидел бы, работая с файлами.
// Своей реализации поиска здесь нет намеренно — вторая реализация означала бы
// вторую выдачу.
//
// Реестр урезан до инструментов графа (GraphRegistry), а Tool ещё и сверяет
// имя с тем же набором: две независимые преграды на пути к `bash`.
type GraphService struct {
	Cfg      *config.Config
	Registry *tools.Registry
	Base     *kb.Base
	Cache    *graph.Cache // nil — граф открывается на каждый вызов
}

var _ kbserve.GraphServer = (*GraphService)(nil)

// Tool выполняет именованный инструмент графа.
//
// Имя сверяется с набором инструментов графа ДО обращения к реестру. Прежде
// проверялось только «включён ли такой инструмент», а реестр был диалоговым:
// запрос `{"name":"bash",…}` исполнял команду от имени службы — без правил
// deny, без подтверждения, а без OLLMCP_TOKEN и без ключа.
func (g *GraphService) Tool(ctx context.Context, collection, name string, args map[string]any) (string, error) {
	if !tools.IsGraphTool(name) {
		return "", fmt.Errorf("%w: %s (графовый вход принимает только %s)",
			kbserve.ErrToolNotServed, name, strings.Join(tools.GraphToolNames(), ", "))
	}
	if g.Registry == nil || !g.Registry.Has(name) {
		return "", fmt.Errorf("инструмент %s на этой службе не включён", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	// Коллекцию навязывает клиент, а не модель: он уже выбрал её у себя,
	// и подставлять другую было бы подменой.
	if collection != "" {
		args["collection"] = collection
	}
	plan, err := g.Registry.Plan(name, args)
	if err != nil {
		return "", err
	}
	return plan.Run(ctx)
}

// Search отдаёт разбираемый результат поиска по графу и цепочку между двумя
// понятиями вопроса.
//
// Нужен подмешиванию карты понятий к вопросу: там текст собирается на стороне
// клиента, потому что от размера карты зависит цена контекста, и решать её
// должен тот, кто за контекст платит. Поэтому размер карты — из запроса
// (mix.entities и mix.neighbors клиента), а ноль — свои настройки службы.
//
// Цепочка — тот же find.Chain, которым подмешивание считает её у себя
// (mixer.Build): у клиента, берущего карту у службы, своего графа нет,
// и без цепочки отсюда вопрос «как связаны X и Y» терял бы её (этап 121, А6).
//
// Годы связей (RelationYears) — тот же «свежайшее N г.», что прямой режим
// печатает по своей коллекции: у клиента службы кусков нет (находка 11 аудита
// 23.10.2026). ctx — запрос клиента: ушёл клиент — ни граф, ни эмбеддер его
// больше не ждут, и место в очереди службы освобождается (находка 12).
func (g *GraphService) Search(ctx context.Context, req kbserve.GraphSearchRequest) (kbserve.GraphSearchResponse, error) {
	coll, gr, _, release, err := g.open(req.Collection)
	if err != nil {
		return kbserve.GraphSearchResponse{}, err
	}
	defer release()
	// Холодный граф открывается десятки секунд: клиент мог уже уйти.
	if err := ctx.Err(); err != nil {
		return kbserve.GraphSearchResponse{}, err
	}

	entities, neighbors := g.Cfg.Mix.Entities, g.Cfg.Mix.Neighbors
	if req.Entities > 0 {
		entities = req.Entities
	}
	if req.Neighbors > 0 {
		neighbors = req.Neighbors
	}
	qv := QueryVector(ctx, gr, g.Cfg, req.Query)
	if err := ctx.Err(); err != nil {
		return kbserve.GraphSearchResponse{}, err
	}
	res := gr.Search(req.Query, graph.SearchOpts{
		TopEntities:  entities,
		TopNeighbors: neighbors,
		TopChunks:    1,
		MinMentions:  2,
		Neighbors:    NeighborRank(g.Cfg),
		Rank:         graph.RankWith(coll),
		QueryVector:  qv,
	})
	chain := find.Chain(gr, res.Entities, res.Relations, find.Opts{})
	return kbserve.GraphSearchResponse{Result: res, Chain: chain,
		RelationYears: RelationYears(coll, res.Relations)}, nil
}

// RelationYears — год самой свежей книги, подтвердившей каждую связь, в порядке
// связей; nil — ни у одной связи года нет.
//
// Счёт тот же, что у отрисовки карты (graph.Render, newestYear): по книгам
// связи (Books), а без них — по подтверждениям (Evidences). Служба отдаёт его
// клиенту, у которого своих кусков нет, и карта через службу печатает тот же
// «свежайшее N г.», что карта по своим файлам.
func RelationYears(src graph.Chunks, rels []graph.FoundRelation) []int {
	if src == nil || len(rels) == 0 {
		return nil
	}
	years := make([]int, len(rels))
	dated := false
	for i, r := range rels {
		keys := r.Books
		if len(keys) == 0 {
			keys = r.Evidences
		}
		for _, k := range keys {
			if info, ok := src.ChunkByRef(k.Doc, k.Ord); ok && info.Book.Year > years[i] {
				years[i] = info.Book.Year
			}
		}
		dated = dated || years[i] > 0
	}
	if !dated {
		return nil
	}
	return years
}

// Graph отдаёт граф коллекции службы и её ранжирование соседей — входу
// /api/v1/find (команды /search и /kb search клиента с kb.server_url).
func (g *GraphService) Graph(collection string) (*graph.Graph, graph.NeighborRank, func(), error) {
	_, gr, _, release, err := g.open(collection)
	if err != nil {
		return nil, graph.NeighborRank{}, nil, err
	}
	return gr, NeighborRank(g.Cfg), release, nil
}

// open открывает граф коллекции службы.
func (g *GraphService) open(collection string) (*kb.Collection, *graph.Graph, string, func(), error) {
	name := collection
	if name == "" {
		name = g.Cfg.KB.Default
	}
	if name == "" {
		names, err := g.Base.Names()
		if err != nil || len(names) == 0 {
			return nil, nil, "", nil, fmt.Errorf("на службе нет коллекций")
		}
		name = names[0]
	}
	coll, err := g.Base.Open(name)
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("коллекция %s: %w", name, err)
	}
	if g.Cache != nil {
		gr, release, err := g.Cache.Get(coll.Dir(), coll.ChunkCount())
		if err != nil {
			return nil, nil, "", nil, fmt.Errorf("граф коллекции %s: %w", name, err)
		}
		return coll, gr, name, release, nil
	}
	gr, err := graph.Open(coll.Dir(), coll.ChunkCount(), g.Cfg.Graph.Rules())
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("граф коллекции %s: %w", name, err)
	}
	return coll, gr, name, func() { gr.Close() }, nil
}

// GraphRegistry собирает набор графового входа службы: только инструменты
// графа, и только те, что включены у администратора.
//
// Набор пересобирается из перечисления, а не фильтруется вычитанием опасного, —
// новый пишущий инструмент не попадёт сюда сам. Реестр диалога содержит то,
// что человек включил себе (bash, запись файлов, правку кода), и отдать его
// службе значило бы исполнять это по сети без правил и подтверждений.
func GraphRegistry(full *tools.Registry) (*tools.Registry, error) {
	if full == nil {
		return nil, nil
	}
	want := make([]string, 0, 5)
	for _, n := range tools.GraphToolNames() {
		if full.Has(n) {
			want = append(want, n)
		}
	}
	return full.Subset(want)
}
