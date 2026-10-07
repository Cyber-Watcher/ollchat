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
	"errors"
	"github.com/Cyber-Watcher/ollchat/internal/steplog"
	"net"
	"net/http"
	"os/signal"
	"strings"
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
		mcpConf = flag.String("mcp-config", "", "файл настроек службы (по умолчанию ollmcp.toml рядом с конфигом ollchat)")
	)
	flag.Usage = usage
	flag.Parse()

	if err := run(*cfgPath, *mcpConf, *addr, *list, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, "ollmcp: "+err.Error())
		os.Exit(1)
	}
}

func run(cfgPath, mcpConf, addr string, list, verbose bool) error {
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
	// Тот же файл настроек, что у ollchat, — те же замечания к нему. Служба
	// живёт под systemd, и опечатка в ключе иначе так и осталась бы
	// невидимой: stderr уходит в журнал службы, где её и найдут.
	for _, msg := range cfg.Warnings {
		fmt.Fprintf(os.Stderr, "ollmcp: предупреждение: %s: %s\n", cfg.Path, msg)
	}

	// Порт службы открывается до сборки: отказ «без ключа — только петля»
	// и занятый порт видны сразу, а не после прогрева графа.
	var ln net.Listener
	var loopback bool
	if addr != "" && !list {
		if ln, loopback, err = kbserve.Listen(addr, kbserve.Token()); err != nil {
			return err
		}
		defer ln.Close()
	}

	stepsPattern, err := cfg.Log.StepsPattern()
	if err != nil {
		return fmt.Errorf("log.steps_file_pattern: %w", err)
	}
	steps := steplog.New(cfg.Log.Dir, stepsPattern, time.Now(), "ollmcp", cfg.Log.Enabled)
	defer steps.Close()

	// Настройки службы: пределы, потолок ответа, срок вызова (этап 109).
	if mcpConf == "" {
		mcpConf = mcp.SettingsPath(path)
	}
	// Служба — это режим --http без --tools: только ей нужен прогретый граф.
	srv, data, err := build(cfg, addr != "" && !list, mcp.ServiceOptions{
		Settings: mcpConf, OutputKB: cfg.Agent.MaxOutputKB, Steps: steps,
	})
	if err != nil {
		return err
	}

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
	err = serveOn(mux, ln, loopback, data.Token, srv)
	if errors.Is(err, errReplaced) {
		// Тот же номер процесса и тот же порт после exec: сторож службы
		// подмены не замечает, а клиенты узнают о возможной смене набора
		// со следующим запросом (internal/mcp/transport.go, этап 109).
		_ = srv.Steps.Close()
		return mcp.Reexec()
	}
	return err
}

// errReplaced — служба остановлена, потому что ~/bin/ollmcp подменён новым
// или правлены её настройки.
var errReplaced = errors.New("бинарь или настройки сменились")

// serveOn поднимает службу на открытом порту и ждёт сигнала останова.
// loopback — порт открыт только петле (kbserve.Listen).
func serveOn(mux *http.ServeMux, ln net.Listener, loopback bool, token string, msrv *mcp.Server) error {
	if token == "" {
		fmt.Fprintln(os.Stderr, "ollmcp: ключ доступа не задан (OLLMCP_TOKEN) — служба только для этой машины")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Сервер общий с ollchat --serve: проверки от чужих веб-страниц и сроки
	// в одном месте.
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
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shut)
	case <-mcp.WatchBinary(ctx, msrv):
		// Начатые запросы доводятся до конца: поиск по книгам идёт секунды,
		// обрывать его ради подмены незачем.
		fmt.Fprintln(os.Stderr, "ollmcp: новый бинарь или настройки — дожидаюсь текущих запросов и перезапускаюсь")
		shut, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = srv.Shutdown(shut)
		return errReplaced
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	// По рунам, а не по байтам: срез s[:100] рвал русскую букву пополам.
	if r := []rune(s); len(r) > 100 {
		return string(r[:100]) + "…"
	}
	return s
}

func usage() {
	fmt.Fprint(os.Stderr, `ollmcp — библиотека книг и граф понятий как служба MCP.

Использование:
  ollmcp                        режим stdio: клиент запускает сервер сам
  ollmcp --http 127.0.0.1:8377  постоянная служба для нескольких клиентов
  ollmcp --tools                показать доступные инструменты и выйти

Пределы параметров, потолок ответа и срок вызова задаются файлом ollmcp.toml
рядом с конфигом ollchat (или --mcp-config). Правка подхватывается сама:
служба перезапускается на новых настройках, клиент получает свежий список.

Настройки берутся из файла ollchat: где лежит база знаний, какая коллекция
по умолчанию, чем считать смыслы, адрес SearXNG.

Все инструменты только читают. Сборка индекса и графа запускается человеком
командами ollchat — служба этого не умеет намеренно.

Флаги:
`)
	flag.PrintDefaults()
}
