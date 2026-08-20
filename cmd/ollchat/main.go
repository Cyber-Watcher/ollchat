// Команда ollchat — TUI-клиент и агент для серверов Ollama.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/chatlog"
	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/session"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
	"github.com/Cyber-Watcher/ollchat/internal/ui"
)

// version — версия приложения, задаётся при сборке через -ldflags.
var version = "1.0.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ollchat: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		cfgPath    = flag.String("c", "", "путь к файлу настроек (по умолчанию "+config.DefaultPath()+")")
		initConfig = flag.Bool("init-config", false, "создать файл настроек с комментариями и выйти")
		serverName = flag.String("server", "", "имя сервера из конфига (перекрывает general.default_server)")
		modelName  = flag.String("model", "", "модель на сервере (перекрывает настройку сервера)")
		workDir    = flag.String("cwd", "", "рабочий каталог — корень песочницы (перекрывает sandbox.root)")
		modeFlag   = flag.String("mode", "", "режим подтверждений: safe, auto-edit или yolo")
		showVer    = flag.Bool("version", false, "показать версию и выйти")

		kbIndex = flag.String("kb-index", "", "собрать коллекцию книг без запуска интерфейса: --kb-index go /путь")
		kbSync  = flag.String("kb-sync", "", "сверить коллекцию с диском без запуска интерфейса")
		kbMerge = flag.String("kb-merge", "", "уплотнить коллекцию: выбросить удалённое, слить сегменты")
		kbEmbed = flag.String("kb-embed", "", "посчитать смыслы коллекции (эмбеддинги) без запуска интерфейса")
		kbDry   = flag.Bool("kb-dry-run", false, "с --kb-embed: только оценить работу, ничего не считая")
		kbList  = flag.Bool("kb-list", false, "показать коллекции базы знаний и выйти")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVer {
		fmt.Println("ollchat " + version)
		return nil
	}

	path := *cfgPath
	if path == "" {
		path = config.DefaultPath()
	}
	path = config.ExpandPath(path)

	if *initConfig {
		if err := config.WriteTemplate(path); err != nil {
			return err
		}
		fmt.Println("Файл настроек создан: " + path)
		fmt.Println("Отредактируйте адреса серверов и запустите ollchat.")
		return nil
	}

	cfg, exists, err := config.Load(path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("файл настроек %s не найден.\nСоздайте его командой: ollchat --init-config", path)
	}

	// Действия с базой знаний идут без интерфейса: обход тысяч книг занимает
	// часы, и держать ради него открытое окно незачем.
	switch {
	case *kbList:
		return runKBList(cfg)
	case *kbIndex != "":
		return runKBIndex(cfg, *kbIndex, flag.Args(), false)
	case *kbSync != "":
		return runKBIndex(cfg, *kbSync, nil, true)
	case *kbMerge != "":
		return runKBMerge(cfg, *kbMerge)
	case *kbEmbed != "":
		return runKBEmbed(cfg, *kbEmbed, *kbDry)
	}

	// Рабочий каталог: флаг важнее конфига.
	root := cfg.Sandbox.Root
	if *workDir != "" {
		root = *workDir
	}
	sandbox, err := permissions.NewSandbox(root, cfg.Sandbox.AllowOutside,
		cfg.Sandbox.FollowSymlinks, cfg.Sandbox.MaxFileKB)
	if err != nil {
		return err
	}
	sandbox.SetMaxPDFMB(cfg.Sandbox.MaxPDFMB)

	ruleSet, err := permissions.Compile(cfg.Permissions.Allow, cfg.Permissions.Ask,
		cfg.Permissions.Deny, sandbox.Root())
	if err != nil {
		return err
	}

	mode := cfg.General.Mode
	if *modeFlag != "" {
		mode = *modeFlag
	}
	guard := permissions.NewGuard(ruleSet, sandbox, mode)
	if err := guard.SetMode(mode); err != nil {
		return err
	}

	// База знаний открывается заранее: инструменты поиска получают её саму,
	// а не путь, и файловой системы не касаются вовсе.
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return fmt.Errorf("база знаний: %w", err)
	}
	defer base.Close()

	// Сервер выбирается раньше набора инструментов: пустой kb.embed_url
	// означает «тот же сервер, что для чата», и его адрес нужен уже здесь.
	// Выбор сервера: флаг важнее конфига.
	srvName := cfg.General.DefaultServer
	if *serverName != "" {
		srvName = *serverName
	}
	srv, ok := cfg.ServerByName(srvName)
	if !ok {
		return fmt.Errorf("сервер %q не описан в файле настроек %s (доступны: %v)",
			srvName, path, cfg.ServerNames())
	}

	model := srv.Model
	if *modelName != "" {
		model = *modelName
	}

	registry, err := tools.NewRegistry(cfg.Agent.Tools, tools.Options{
		Sandbox:        sandbox,
		BashTimeout:    cfg.Agent.BashTimeoutDuration(),
		MaxOutputKB:    cfg.Agent.MaxOutputKB,
		KB:             base,
		KBDir:          cfg.KB.Dir,
		KBDefault:      cfg.KB.Default,
		KBTopK:         cfg.KB.TopK,
		KBMaxPerBook:   cfg.KB.MaxPerBook,
		Semantic:       cfg.KB.Semantic,
		MinCosine:      cfg.KB.MinCosine,
		SemanticWeight: cfg.KB.SemanticWeight,
		AnswerStyle:    cfg.KB.AnswerStyle,
		SearxURL:       cfg.Web.SearxngURL,
		SearxTimeout:   cfg.Web.TimeoutDuration(),
		Embedder:       kbembed.New(cfg, srv.URL, srv.TimeoutDuration(), srv.Headers),
	})
	if err != nil {
		return err
	}

	logPattern, err := cfg.Log.NamePattern()
	if err != nil {
		return fmt.Errorf("log.file_pattern: %w", err)
	}
	logger := chatlog.NewFromPattern(cfg.Log.Dir, logPattern, time.Now(), cfg.Log.Enabled)
	defer logger.Close()
	if err := logger.WriteSessionHeader(srv.Name, srv.URL, model); err != nil {
		fmt.Fprintln(os.Stderr, "предупреждение: не удалось писать журнал: "+err.Error())
	}

	store := session.NewStore(sessionDir())

	m := ui.New(cfg, guard, registry, logger, store, srv, model)
	// Полноэкранный режим и режим мыши в Bubble Tea v2 задаются самим экраном
	// (полями AltScreen и MouseMode в Model.View), а не настройками программы:
	// мышь должна переключаться на лету, иначе текст ленты не выделить.
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}

// sessionDir возвращает каталог для сохранённых сессий.
func sessionDir() string {
	if dir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(dir, ".local", "share", "ollchat", "sessions")
	}
	return filepath.Join(os.TempDir(), "ollchat-sessions")
}

func usage() {
	fmt.Fprintf(os.Stderr, `ollchat %s — TUI-клиент и агент для Ollama

Использование:
  ollchat [флаги]

Флаги:
`, version)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Примеры:
  ollchat --init-config           создать файл настроек
  ollchat                         запуск с сервером по умолчанию
  ollchat --server lab            запуск с указанным сервером
  ollchat --model qwen3.6:latest  запуск с указанной моделью
  ollchat --cwd ~/projects/app    песочница в другом каталоге

База знаний по книгам:
  ollchat --kb-index go /mnt/books/Go   собрать коллекцию (можно под nohup)
  ollchat --kb-sync go                  доиндексировать новое, убрать пропавшее
  ollchat --kb-merge go                 уплотнить: выбросить удалённое с диска
  ollchat --kb-embed go --kb-dry-run    оценить работу по смыслам
  ollchat --kb-embed go                 посчитать смыслы (можно под nohup)
  ollchat --kb-list                     что уже собрано
`)
}
