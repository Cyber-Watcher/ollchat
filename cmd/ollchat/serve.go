package main

import (
	"context"
	"encoding/json"
	"fmt"
	gmaint "github.com/Cyber-Watcher/ollchat/internal/graph/maint"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
	"github.com/Cyber-Watcher/ollchat/internal/qtranslate"
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
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
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

	// Порт открывается первым делом: отказ «без ключа — только петля»
	// и занятый порт должны быть видны сразу, а не после сборки службы.
	token := kbserve.Token()
	ln, loopback, err := kbserve.Listen(addr, token)
	if err != nil {
		return err
	}
	defer ln.Close()

	mux, names, closeSvc, err := serveMux(cfg, token, withMCP, registry, base, cache)
	if err != nil {
		return err
	}
	defer closeSvc()

	// Об отсутствии ключа говорим прямо и один раз при запуске. Без ключа
	// служба открыта только петле (kbserve.Listen), и человек должен знать,
	// почему соседи её не видят.
	fmt.Fprintf(os.Stderr, "ollchat --serve %s: коллекций %d, ключ доступа %s, MCP %s\n",
		addr, len(names),
		map[bool]string{true: "задан", false: "не задан (OLLMCP_TOKEN) — служба только для этой машины"}[token != ""],
		map[bool]string{true: "включён (/mcp)", false: "выключен"}[withMCP])

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Сервер общий с ollmcp: проверки от чужих веб-страниц (Origin, имя
	// машины на петле, JSON в теле) и сроки — в одном месте.
	srv := kbserve.NewHTTPServer(mux, loopback)
	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
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

// serveMux собирает маршруты службы: вход данных, граф и, если попросили,
// MCP. Возвращает имена коллекций для строки запуска и закрытие журнала шагов.
func serveMux(cfg *config.Config, token string, withMCP bool, registry *tools.Registry,
	base *kb.Base, cache *graph.Cache) (*http.ServeMux, []string, func(), error) {

	if base == nil {
		return nil, nil, nil, fmt.Errorf("библиотека не открыта: укажите kb.dir в файле настроек")
	}
	names, err := base.Names()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("библиотека %s: %w", cfg.KB.Dir, err)
	}
	if len(names) == 0 {
		return nil, nil, nil, fmt.Errorf("в библиотеке %s нет коллекций — сначала соберите её: "+
			"ollchat --kb-add имя /путь/к/книгам", cfg.KB.Dir)
	}

	// Вектор вопроса служба считает через первый сервер конфига — и с его
	// заголовками, как диалог со своим сервером. Без них Ollama за прокси
	// с авторизацией отказывала эмбеддеру, и служба молча откатывалась
	// на поиск по словам.
	fallback := ""
	var headers map[string]string
	if len(cfg.Servers) > 0 {
		fallback, headers = cfg.Servers[0].URL, cfg.Servers[0].Headers
	}

	// Графовому входу — только инструменты графа. Реестр диалога содержит то,
	// что человек включил себе (bash, запись файлов, правку кода), и отдать
	// его службе значило бы исполнять это по сети без правил и подтверждений.
	graphReg, err := gmaint.GraphRegistry(registry)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("набор графового входа службы: %w", err)
	}

	mux := kbserve.Handler(kbserve.Opts{
		TableBoost: cfg.KB.TableBoost,
		Reranker:   kbrerank.New(cfg.KB.RerankOptions()),
		RerankOpts: kb.RerankOpts{Candidates: cfg.KB.RerankCandidates, Snippet: cfg.KB.RerankSnippet},
		Base:       base,
		Emb:        kbembed.New(cfg.KB.EmbedOptions(), fallback, 0, headers),
		Translator: qtranslate.New(cfg, fallback, headers),
		Default:    cfg.KB.Default,
		Token:      token,
		Graph:      &gmaint.GraphService{Cfg: cfg, Registry: graphReg, Base: base, Cache: cache},
		Verbose:    true,
	})

	// Вход MCP на том же порту, если попросили. Клиенты MCP ходят в /mcp,
	// клиенты ollchat — в /api/v1; две службы с двумя портами и двумя ключами
	// ради этого заводить незачем.
	closeSvc := func() {}
	if withMCP {
		// **Отдельный реестр, урезанный до чтения.** Реестр диалога содержит
		// то, что человек включил себе: bash, запись файлов, правку кода.
		// Раздать его по сети значило бы отдать чужие руки на сервер:
		// подтвердить опасное действие тут некому, а ключ есть у всех сотрудников.
		ro, err := readOnlyRegistry(registry)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("набор службы MCP: %w", err)
		}
		// Сборка та же, что у ollmcp (mcp.NewService): пределы, срок вызова
		// и потолок ответа из ollmcp.toml рядом с конфигом и журнал шагов.
		// Прежде здесь стоял голый NewServer, и тот же набор по /mcp шёл
		// без всего этого.
		stepsPattern, err := cfg.Log.StepsPattern()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("log.steps_file_pattern: %w", err)
		}
		steps := steplog.New(cfg.Log.Dir, stepsPattern, time.Now(), "ollchat-serve", cfg.Log.Enabled)
		pruneSteps(steps, cfg)
		settings := ""
		if cfg.Path != "" {
			settings = mcp.SettingsPath(cfg.Path)
		}
		msrv, err := mcp.NewService(ro, nil, mcp.ServiceOptions{
			Settings: settings, OutputKB: cfg.Agent.MaxOutputKB, Steps: steps,
		})
		if err != nil {
			_ = steps.Close()
			return nil, nil, nil, fmt.Errorf("служба MCP: %w", err)
		}
		closeSvc = func() { _ = steps.Close() }
		mcp.MountHTTP(mux, msrv, token, true)
	}

	// Проверка живости для systemd и для человека: «служба вообще поднялась?»
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "ollchat", "version": version,
			"collections": len(names), "auth": token != "", "mcp": withMCP,
		})
	})
	return mux, names, closeSvc, nil
}

// Графовый вход службы — gmaint.GraphService: его зовут и `ollchat --serve`,
// и `ollmcp --http`, поэтому он живёт в internal/graph/maint, а не здесь
// (этап 121, А6).

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
