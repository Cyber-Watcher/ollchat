// Команда ollmcp — библиотека книг и граф понятий как служба MCP.
//
// Зачем отдельная программа. Поиск по библиотеке и граф понятий доступны модели
// внутри ollchat, но это знание нужно и другим: другому ассистенту, скрипту,
// самому владельцу из командной строки. Служба работает независимо от того,
// запущен ollchat или нет, — как поисковая служба, к которой ходят разные
// клиенты.
//
// Два способа подключения, оба обязательны:
//
//	ollmcp                          режим stdio: клиент запускает сервер сам
//	ollmcp --http 127.0.0.1:8377    постоянная служба, много клиентов разом
//
// Инструменты только на чтение. Сборки, индексации и векторизации здесь нет
// намеренно: они занимают видеокарту на часы, и право запускать их остаётся
// у человека. Это то же правило, по которому у модели в ollchat нет инструмента
// сборки графа.
package main

import (
	"context"
	"encoding/json"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"flag"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
	"github.com/Cyber-Watcher/ollchat/internal/mcp"
	"os"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

func main() {
	var (
		cfgPath = flag.String("c", "", "путь к файлу настроек ollchat (по умолчанию "+config.DefaultPath()+")")
		addr    = flag.String("http", "", "слушать HTTP по этому адресу; пусто — режим stdio")
		list    = flag.Bool("tools", false, "показать список инструментов и выйти")
		verbose = flag.Bool("v", false, "писать обращения клиентов в поток ошибок")
	)
	flag.Usage = usage
	flag.Parse()

	if err := run(*cfgPath, *addr, *list, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, "ollmcp: "+err.Error())
		os.Exit(1)
	}
}

func run(cfgPath, addr string, list, verbose bool) error {
	path := cfgPath
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, exists, err := config.Load(path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("файл настроек %s не найден.\nСоздайте его командой: ollchat --init-config", path)
	}

	srv, data, err := build(cfg)
	if err != nil {
		return err
	}
	stepsPattern, err := cfg.Log.StepsPattern()
	if err != nil {
		return fmt.Errorf("log.steps_file_pattern: %w", err)
	}
	srv.Steps = steplog.New(cfg.Log.Dir, stepsPattern, time.Now(), "ollmcp", cfg.Log.Enabled)
	defer srv.Steps.Close()

	if list {
		for _, t := range srv.Tools() {
			fmt.Printf("%-16s %s\n", t.Name, firstLine(t.Description))
		}
		return nil
	}

	// Режим stdio — то, ради чего эта программа и существует отдельно:
	// клиент MCP запускает её сам и говорит через стандартные потоки, а
	// `ollchat --serve` так не умеет и уметь не должен — он служба.
	if addr == "" {
		return mcp.ServeStdio(srv, verbose)
	}

	// По сети оба протокола живут на одном порту: клиенты MCP ходят в /mcp,
	// клиенты ollchat — в /api/v1. Две службы с двумя портами и двумя ключами
	// ради этого заводить незачем.
	mux := kbserve.Handler(data)
	mcp.MountHTTP(mux, srv, data.Token, verbose)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mcp.Info(srv))
	})
	return serveOn(mux, addr, data.Token)
}

// serveOn поднимает службу и ждёт сигнала останова.
func serveOn(mux *http.ServeMux, addr, token string) error {
	if token == "" {
		fmt.Fprintln(os.Stderr, "ollmcp: ключ доступа НЕ ЗАДАН (OLLMCP_TOKEN)")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
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
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shut)
	}
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
		_ = i
	}
	if len(s) > 100 {
		return s[:100] + "…"
	}
	return s
}

func usage() {
	fmt.Fprint(os.Stderr, `ollmcp — библиотека книг и граф понятий как служба MCP.

Использование:
  ollmcp                        режим stdio: клиент запускает сервер сам
  ollmcp --http 127.0.0.1:8377  постоянная служба для нескольких клиентов
  ollmcp --tools                показать доступные инструменты и выйти

Настройки берутся из файла ollchat: где лежит база знаний, какая коллекция
по умолчанию, чем считать смыслы, адрес SearXNG.

Все инструменты только читают. Сборка индекса и графа запускается человеком
командами ollchat — служба этого не умеет намеренно.

Флаги:
`)
	flag.PrintDefaults()
}
