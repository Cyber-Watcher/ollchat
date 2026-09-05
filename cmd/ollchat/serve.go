package main

import (
	"context"
	"encoding/json"
	"fmt"
	gmaint "github.com/Cyber-Watcher/ollchat/internal/graph/maint"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
	"github.com/Cyber-Watcher/ollchat/internal/mcp"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// Сервер знаний: `ollchat --serve`.
//
// **Почему тем же бинарём.** Администратор уже держит `ollchat`: им он
// индексирует книги (`--kb-add`), считает смыслы (`--kb-embed`) и строит граф
// (`--graph-build`). Отдельная программа-сервер означала бы второй файл, вторую
// версию и вторую возможность их рассогласовать. Скачал один файл — собрал
// знание — раздал его: главная идея проекта не нарушена, а расширена.
//
// `ollmcp` при этом никуда не девается: он для сторонних клиентов MCP и
// поднимает **тот же самый** серверный пакет.
//
// **Что раздаётся.** Книги (словесный и смысловой поиск, слияние, чтение куска
// целиком) и граф понятий — всё то же и теми же числами, что локально. Клиент
// не отличает.
//
// **Что не раздаётся никогда.** Запись: индексация, счёт смыслов и сборка
// графа остаются действиями администратора над своими файлами. Служба
// поднимается только из инструментов чтения, и другого набора у неё нет.

// runServe поднимает службу знаний.
func runServe(cfg *config.Config, addr string, withMCP bool, registry *tools.Registry,
	base *kb.Base, cache *graph.Cache) error {

	if base == nil {
		return fmt.Errorf("библиотека не открыта: укажите kb.dir в файле настроек")
	}
	names, err := base.Names()
	if err != nil {
		return fmt.Errorf("библиотека %s: %w", cfg.KB.Dir, err)
	}
	if len(names) == 0 {
		return fmt.Errorf("в библиотеке %s нет коллекций — сначала соберите её: "+
			"ollchat --kb-add имя /путь/к/книгам", cfg.KB.Dir)
	}

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	token := kbserve.Token()

	mux := kbserve.Handler(kbserve.Opts{
		TableBoost: cfg.KB.TableBoost,
		Reranker:   kbrerank.New(cfg.KB.RerankOptions()),
		RerankOpts: kb.RerankOpts{Candidates: cfg.KB.RerankCandidates, Snippet: cfg.KB.RerankSnippet},
		Base:       base,
		Emb:        kbembed.New(cfg.KB.EmbedOptions(), fallback, 0, nil),
		Default:    cfg.KB.Default,
		Token:      token,
		Graph:      &graphService{cfg: cfg, registry: registry, base: base, cache: cache},
		Verbose:    true,
	})

	// Вход MCP на том же порту, если попросили. Клиенты MCP ходят в /mcp,
	// клиенты ollchat — в /api/v1; две службы с двумя портами и двумя ключами
	// ради этого заводить незачем.
	if withMCP {
		// **Отдельный реестр, урезанный до чтения.** Реестр диалога содержит
		// то, что человек включил себе: bash, запись файлов, правку кода.
		// Раздать его по сети значило бы отдать чужие руки на сервер:
		// подтвердить опасное действие тут некому, а ключ есть у всех сотрудников.
		ro, err := readOnlyRegistry(registry)
		if err != nil {
			return fmt.Errorf("набор службы MCP: %w", err)
		}
		mcp.MountHTTP(mux, mcp.NewServer(ro), token, true)
	}

	// Проверка живости для systemd и для человека: «служба вообще поднялась?»
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "ollchat", "version": version,
			"collections": len(names), "auth": token != "", "mcp": withMCP,
		})
	})

	// Об отсутствии ключа говорим прямо и один раз при запуске. Молчаливая
	// служба без проверки в корпоративной сети — это не «удобно настроено»,
	// а незамеченная дыра.
	fmt.Fprintf(os.Stderr, "ollchat --serve %s: коллекций %d, ключ доступа %s, MCP %s\n",
		addr, len(names),
		map[bool]string{true: "задан", false: "НЕ ЗАДАН (OLLMCP_TOKEN)"}[token != ""],
		map[bool]string{true: "включён (/mcp)", false: "выключен"}[withMCP])

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errc <- err
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		// Останов даётся время: оборвать поиск на полуслове значит отдать
		// клиенту пустоту вместо выдачи.
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		fmt.Fprintln(os.Stderr, "ollchat --serve: останавливаюсь")
		return srv.Shutdown(shut)
	}
}

// graphService отвечает на вопросы к графу от имени службы.
//
// Инструменты берутся из **того же реестра**, что у локального ollchat: клиент
// обязан получить ровно тот текст, который увидел бы, работая с файлами.
// Своей реализации поиска здесь нет намеренно — вторая реализация означала бы
// вторую выдачу.
type graphService struct {
	cfg      *config.Config
	registry *tools.Registry
	base     *kb.Base
	cache    *graph.Cache
}

// Tool выполняет именованный инструмент графа.
func (g *graphService) Tool(ctx context.Context, collection, name string, args map[string]any) (string, error) {
	if g.registry == nil || !g.registry.Has(name) {
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
	plan, err := g.registry.Plan(name, args)
	if err != nil {
		return "", err
	}
	return plan.Run(ctx)
}

// Search отдаёт разбираемый результат поиска по графу.
//
// Нужен подмешиванию карты понятий к вопросу: там текст собирается на стороне
// клиента, потому что от размера карты зависит цена контекста, и решать её
// должен тот, кто за контекст платит.
func (g *graphService) Search(ctx context.Context, collection, query string) (any, error) {
	coll, gr, _, release, err := openGraphFor(g, collection)
	if err != nil {
		return nil, err
	}
	defer release()

	res := gr.Search(query, graph.SearchOpts{
		TopEntities:  g.cfg.Mix.Entities,
		TopNeighbors: g.cfg.Mix.Neighbors,
		TopChunks:    1,
		MinMentions:  2,
		Neighbors:    gmaint.NeighborRank(g.cfg),
		Rank:         graph.RankWith(coll),
		QueryVector:  gmaint.QueryVector(gr, g.cfg, query),
	})
	return res, nil
}

// openGraphFor открывает граф коллекции службы.
func openGraphFor(g *graphService, collection string) (*kb.Collection, *graph.Graph, string, func(), error) {
	name := collection
	if name == "" {
		name = g.cfg.KB.Default
	}
	if name == "" {
		names, err := g.base.Names()
		if err != nil || len(names) == 0 {
			return nil, nil, "", nil, fmt.Errorf("на службе нет коллекций")
		}
		name = names[0]
	}
	coll, err := g.base.Open(name)
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("коллекция %s: %w", name, err)
	}
	if g.cache != nil {
		gr, release, err := g.cache.Get(coll.Dir(), coll.ChunkCount())
		if err != nil {
			return nil, nil, "", nil, fmt.Errorf("граф коллекции %s: %w", name, err)
		}
		return coll, gr, name, release, nil
	}
	gr, err := graph.Open(coll.Dir(), coll.ChunkCount(), g.cfg.Graph.Rules())
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("граф коллекции %s: %w", name, err)
	}
	return coll, gr, name, func() { gr.Close() }, nil
}

// readOnlyRegistry собирает набор инструментов службы: только чтение.
//
// Пересобирается заново, а не фильтруется из готового: фильтр пришлось бы
// держать в согласии с реестром, а рассогласование здесь означало бы `bash`,
// открытый в сеть.
func readOnlyRegistry(full *tools.Registry) (*tools.Registry, error) {
	want := make([]string, 0, 8)
	for _, n := range tools.ReadOnlyNames() {
		// Берём лишь то, что и так включено у администратора: выключенный
		// web_search не должен появляться в службе только потому, что он
		// значится в списке безопасных.
		if full != nil && full.Has(n) {
			want = append(want, n)
		}
	}
	return full.Subset(want)
}
