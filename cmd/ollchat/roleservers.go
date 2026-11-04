package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/roleserver"
)

// printRoleServers — ключ --role-servers: на какие адреса пойдут сборка графа
// и счёт векторов по этому конфигу, строками через табуляцию (этап 123).
//
// Читает его обвязка (privatescripts/bin/graph-lib.sh): до этапа 123 она сама
// разбирала [graph].url и [[graph.nodes]] через sed и awk, а с [graph].server
// и [[servers.nodes]] это стал бы второй разборщик конфига, способный разойтись
// с первым (решение владельца 04.11.2026: обвязка спрашивает ollchat). Своих
// правил здесь нет: поля и запасной адрес те же, что у сборки (graphrun.go:
// пустой [graph].url — cfg.EmbedFallback()).
//
//	graph  <сервер|->  <адрес>  <модель|->
//	node   <имя>  <адрес>  <слотов>  <probe|->   — по строке на узел пула
//	embed  <сервер|->  <адрес|->  <модель|->     — модель пуста: векторы выключены
func printRoleServers(w io.Writer, cfg *config.Config) error {
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	gurl := cfg.Graph.URL
	if gurl == "" {
		gurl = cfg.EmbedFallback()
	}
	if _, err := fmt.Fprintf(w, "graph\t%s\t%s\t%s\n", dash(cfg.Graph.Server), dash(gurl), dash(cfg.Graph.Model)); err != nil {
		return err
	}
	for _, n := range cfg.Graph.ExtractNodes() {
		if _, err := fmt.Fprintf(w, "node\t%s\t%s\t%d\t%s\n", n.Name, n.URL, n.Workers, dash(n.Probe)); err != nil {
			return err
		}
	}
	eurl := cfg.KB.EmbedURL
	if eurl == "" {
		eurl = cfg.EmbedFallback()
	}
	_, err := fmt.Fprintf(w, "embed\t%s\t%s\t%s\n", dash(cfg.KB.EmbedServer), dash(eurl), dash(cfg.KB.EmbedModel))
	return err
}

// useRoleServer — ключи --use-graph-server, --use-embed-server и
// --use-chat-server: та же проверка и та же запись одной строкой, что у
// /graph-server и /embed-server в TUI (пакет roleserver), — для обвязки,
// скриптов и меня, чтобы сервер не менялся правкой конфига руками.
//
// Замечания идут в w строками «замечание: …», ход — в errw. Проверка не прошла
// без --use-force, или идёт работа с коллекцией, — ошибка, и код выхода не 0:
// скрипт узнаёт отказ, не разбирая текст. Под идущей работой --use-force
// не помогает (roleserver.CheckIdle).
func useRoleServer(w, errw io.Writer, cfg *config.Config, kind roleserver.Kind, name string, force bool) error {
	if cfg.Path == "" {
		return errors.New("конфиг прочитан не из файла — выбор записать некуда")
	}
	var collDirs []string
	if kind != roleserver.Chat {
		base, err := kb.OpenBase(cfg.KB.Dir)
		if err != nil {
			return fmt.Errorf("база знаний: %w", err)
		}
		collDirs, err = roleserver.CollDirs(base)
		base.Close()
		if err != nil {
			return fmt.Errorf("коллекции не перечислены, сверять выбор не с чем: %w", err)
		}
	}
	job, err := roleserver.NewJob(cfg, kind, name, collDirs)
	if err != nil {
		return err
	}
	if err := roleserver.CheckIdle(kind, job.Server.Name, collDirs); err != nil {
		return err
	}
	fmt.Fprintf(errw, "проверяю сервер %s для %s (не дольше %d с)…\n",
		job.Server.Name, kind.Title(), int(roleserver.Timeout.Seconds()))
	r := job.Run()
	for _, n := range r.Notes {
		fmt.Fprintln(w, "замечание: "+n)
	}
	if r.Err != nil && !force {
		return fmt.Errorf("сервер %s для %s не выбран: %w\nзаписать всё равно: %s %s --use-force",
			r.Server, kind.Title(), r.Err, kind.Flag(), r.Server)
	}
	// Проверка идёт до минуты: сборка могла начаться за это время.
	if err := roleserver.CheckIdle(kind, r.Server, collDirs); err != nil {
		return err
	}
	if r.Err != nil {
		fmt.Fprintln(w, "замечание: проверка не прошла, записываю по --use-force: "+r.Err.Error())
	}
	if err := roleserver.Write(cfg.Path, kind, r.Server, "выбран из ollchat: "+kind.Flag()); err != nil {
		return fmt.Errorf("выбор не записан: %w", err)
	}
	for _, o := range roleserver.Overrides(cfg.Path) {
		fmt.Fprintln(w, "замечание: "+o)
	}
	fmt.Fprintf(w, "записано: %s = %q в %s\n", kind.Key(), r.Server, cfg.Path)
	switch kind {
	case roleserver.Graph:
		fmt.Fprintln(w, "следующая сборка (graph put, --graph-build) и ночной запуск пойдут туда")
	case roleserver.Embed:
		fmt.Fprintln(w, "индексация, сборка графа и поиск возьмут его при следующем запуске; "+
			"служба ollmcp читает конфиг при запуске — у неё новый адрес после перезапуска")
	case roleserver.Chat:
		fmt.Fprintln(w, "ollchat без --server откроет чат на нём")
	}
	return nil
}
