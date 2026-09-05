package maint

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Архив коллекции с графом и восстановление — команды --graph-archive,
// --graph-archives и --graph-restore. Ядро — internal/graph/archive.go.

// archiveOpts собирает настройки архива из раздела графа.
func archiveOpts(cfg *config.Config) graph.ArchiveOpts {
	return graph.ArchiveOpts{Dir: cfg.Graph.ArchiveDirPath(), Keep: cfg.Graph.ArchiveKeep}
}

// Archive снимает архив коллекции руками. Отказ, пока с коллекцией идёт
// работа: ждать её часами не дело этой команды, а идти под ней нельзя.
func Archive(stdout io.Writer, cfg *config.Config, name string) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	dir := base.CollectionDir(name)
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		return fmt.Errorf("нет коллекции %s (каталог %s)", name, dir)
	}
	o := archiveOpts(cfg)
	fmt.Fprintf(stdout, "архив коллекции %s → %s (это до минуты)…\n", name, o.Dir)
	res, err := graph.Archive(dir, o)
	if err != nil {
		if errors.Is(err, graph.ErrBusy) {
			return fmt.Errorf("%v.\nАрхив снимается только со спокойной коллекции: дождитесь конца работы и повторите", err)
		}
		return err
	}
	printArchiveResult(stdout, name, res)
	return nil
}

// printArchiveResult — одна форма отчёта на команду и на чат.
func printArchiveResult(w io.Writer, name string, res graph.ArchiveResult) {
	fmt.Fprintln(w, ArchiveSummary(name, res))
}

// ArchiveSummary — итог архива одной строкой с продолжением: что, куда,
// сколько, и что убрано по ротации.
func ArchiveSummary(name string, res graph.ArchiveResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "коллекция %s заархивирована: %s → %s за %s\n  %s",
		name, fsx.HumanSize(res.Bytes), fsx.HumanSize(res.Wrote),
		res.Elapsed.Round(time.Second), res.Path)
	if len(res.Removed) > 0 {
		fmt.Fprintf(&b, "\n  по ротации убрано старых: %d", len(res.Removed))
	}
	return b.String()
}

// Archives перечисляет архивы: name — коллекция, "all" или пусто — все.
func Archives(stdout io.Writer, cfg *config.Config, name string) error {
	if name == "all" || name == "все" {
		name = ""
	}
	fmt.Fprint(stdout, ArchivesList(cfg, name))
	return nil
}

// ArchivesList — перечень архивов текстом; общий для команды и чата.
func ArchivesList(cfg *config.Config, name string) string {
	dir := cfg.Graph.ArchiveDirPath()
	list, err := graph.Archives(dir, name)
	if err != nil {
		return fmt.Sprintf("каталог архивов %s не читается: %v\n", dir, err)
	}
	if len(list) == 0 {
		what := "архивов"
		if name != "" {
			what = "архивов коллекции " + name
		}
		return fmt.Sprintf("в %s %s нет\n", dir, what)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "архивы в %s (новые первыми):\n", dir)
	var total int64
	for _, a := range list {
		tag := ""
		if a.Tag != "" {
			tag = "  " + a.Tag
		}
		fmt.Fprintf(&b, "  %s  %-12s %8s%s  %s\n", a.Time.Format("02.01.2006 15:04"),
			a.Collection, fsx.HumanSize(a.Size), tag, filepath.Base(a.Path))
		total += a.Size
	}
	fmt.Fprintf(&b, "  всего %d, %s\n", len(list), fsx.HumanSize(total))
	return b.String()
}

// Restore восстанавливает коллекцию из архива, подменяя нынешнюю.
//
// Прежняя коллекция перед подменой уходит в архив с пометкой before-restore,
// так что ничего не теряется. Подтверждение словом — как у уплотнения:
// действие намеренное, а «не тот файл» здесь стоит рабочего графа.
func Restore(stdout io.Writer, cfg *config.Config, archive string, yes bool) error {
	archive = config.ExpandPath(archive)
	if _, err := os.Stat(archive); err != nil {
		// Имя без пути — ищем в каталоге архивов: так короче в командах.
		alt := filepath.Join(cfg.Graph.ArchiveDirPath(), archive)
		if _, err2 := os.Stat(alt); err2 != nil {
			return fmt.Errorf("архив %s не найден (смотрел и в %s)", archive, cfg.Graph.ArchiveDirPath())
		}
		archive = alt
	}
	fmt.Fprintf(stdout, "читаю оглавление %s…\n", archive)
	peek, err := graph.PeekArchive(archive)
	if err != nil {
		return err
	}
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	target := base.CollectionDir(peek.Collection)

	fmt.Fprintf(stdout, "в архиве коллекция %s, файлов %d", peek.Collection, peek.Files)
	describeGraphs(stdout, peek.Graphs)
	exists := false
	if _, err := os.Stat(target); err == nil {
		exists = true
		if b := graph.Busy(target); b != "" {
			return fmt.Errorf("с коллекцией %s идёт работа: %s.\nВосстанавливать из-под неё нельзя — дождитесь конца", peek.Collection, b)
		}
		fmt.Fprintf(stdout, "сейчас на диске: %s", target)
		cur := map[string]graph.Meta{}
		for _, gd := range graph.GraphDirs(target) {
			if m, err := graph.Stat(gd); err == nil {
				cur[filepath.Base(gd)] = m
			}
		}
		describeGraphs(stdout, cur)
		fmt.Fprintln(stdout, "перед подменой нынешняя коллекция уйдёт в архив с пометкой before-restore")
	} else {
		fmt.Fprintf(stdout, "коллекции %s на диске нет — встанет из архива\n", peek.Collection)
	}
	fmt.Fprintln(stdout, "ollchat и ollmcp, держащие эту коллекцию открытой, надо закрыть: они продолжат читать прежние файлы")

	if err := confirmRestore(peek.Collection, exists, yes); err != nil {
		return err
	}

	start := time.Now()
	fmt.Fprintln(stdout, "восстанавливаю…")
	res, err := graph.Restore(archive, base.CollectionsDir(), archiveOpts(cfg))
	if err != nil {
		return err
	}
	if res.Backup != "" {
		fmt.Fprintf(stdout, "прежняя коллекция сохранена: %s\n", res.Backup)
	}
	fmt.Fprintf(stdout, "коллекция %s восстановлена в %s: файлов %d, %s, за %s\n",
		res.Collection, res.Dir, res.Files, fsx.HumanSize(res.Bytes), time.Since(start).Round(time.Second))
	fmt.Fprintf(stdout, "проверка: ollchat --graph-doctor %s\n", res.Collection)
	return nil
}

func describeGraphs(w io.Writer, graphs map[string]graph.Meta) {
	if len(graphs) == 0 {
		fmt.Fprintln(w, "; графа нет")
		return
	}
	fmt.Fprintln(w)
	for dir, m := range graphs {
		fmt.Fprintf(w, "  граф %s: понятий %d, связей %d, разобрано кусков %d из %d, обновлён %s\n",
			dir, m.Entities, m.Edges, m.Covered, m.Chunks, m.Updated.Format("02.01.2006 15:04"))
	}
}

// confirmRestore — подтверждение словом, как у уплотнения (kb/maint).
func confirmRestore(name string, exists bool, yes bool) error {
	if yes {
		fmt.Fprintln(os.Stdout, "--graph-restore-yes: подтверждение пропущено.")
		return nil
	}
	if !isTTY(os.Stdin) {
		return fmt.Errorf("восстановление требует подтверждения, а ввод не с терминала;\n" +
			"в скрипте добавьте --graph-restore-yes, если действительно этого хотите")
	}
	question := fmt.Sprintf("Поставить коллекцию %s из архива?", name)
	if exists {
		question = fmt.Sprintf("Подменить коллекцию %s тем, что в архиве?", name)
	}
	fmt.Fprintf(os.Stdout, "\n%s\nНапишите ДА (заглавными) и нажмите Enter: ", question)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("подтверждение не получено: %w", err)
	}
	if strings.TrimSpace(line) != "ДА" {
		return fmt.Errorf("восстановление отменено: коллекция осталась прежней")
	}
	return nil
}
