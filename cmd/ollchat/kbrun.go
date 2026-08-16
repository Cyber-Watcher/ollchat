package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
)

// Безголовая индексация.
//
// Держать окно приложения открытым сутки ради обхода тысяч книг — плохая идея:
// случайное закрытие терминала обрывает работу, а следить за прогрессом всё
// равно незачем. Поэтому те же действия доступны отдельной командой:
//
//	nohup ollchat --kb-index go /путь/к/книгам &
//	ollchat --kb-sync go
//	ollchat --kb-list
//
// Ctrl+C прерывает работу так же, как Esc в приложении: текущая книга
// доводится до целого состояния, остальное продолжится со следующего запуска.

// runKBIndex собирает или обновляет коллекцию без запуска интерфейса.
func runKBIndex(cfg *config.Config, name string, paths []string, sync bool) error {
	if err := kb.ValidName(name); err != nil {
		return err
	}
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		if sync {
			return err
		}
		if coll, err = base.Create(name, ""); err != nil {
			return err
		}
	}

	if !sync {
		allowed, err := allowedPaths(cfg, paths)
		if err != nil {
			return err
		}
		if err := coll.AddRoots(allowed); err != nil {
			return err
		}
		paths = allowed
	}

	// Ctrl+C прерывает работу, а не убивает её: индексация успевает
	// зафиксировать текущую книгу.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	report := progressPrinter()
	opt := kb.IndexOpts{Workers: cfg.KB.Workers, MaxBytes: int64(cfg.KB.MaxBookMB) << 20}

	var res kb.IndexResult
	if sync {
		res, err = coll.Sync(ctx, report)
	} else {
		res, err = coll.Add(ctx, paths, opt, report)
	}
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}

	st := coll.Stats()
	fmt.Printf("коллекция %s: добавлено книг %d", name, res.Added)
	if res.Removed > 0 {
		fmt.Printf(", убрано %d", res.Removed)
	}
	if res.Scans > 0 {
		fmt.Printf(", сканов без текста %d", res.Scans)
	}
	if res.Errors > 0 {
		fmt.Printf(", сбоев %d", res.Errors)
	}
	fmt.Printf("\nвсего: книг %d, кусков %d, термов %d, на диске %.1f МБ, за %s\n",
		st.Indexed, st.Chunks, st.Terms, float64(st.Bytes)/1e6, time.Since(start).Round(time.Second))
	if res.Canceled {
		fmt.Println("работа прервана; продолжить: та же команда — она перечитает только недостающее")
	}
	return nil
}

// runKBMerge уплотняет коллекцию без запуска интерфейса.
//
// Держать окно открытым ради переписывания четверти миллиона кусков незачем,
// а Ctrl+C безопасен: коллекция остаётся прежней целиком.
func runKBMerge(cfg *config.Config, name string) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, err := coll.Merge(ctx, progressPrinter())
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	if res.Canceled {
		fmt.Println("уплотнение прервано; коллекция осталась прежней")
		return nil
	}
	fmt.Printf("коллекция %s уплотнена за %s\n", name, res.Elapsed.Round(time.Second))
	fmt.Printf("  книг выброшено: %d\n", res.BooksDropped)
	fmt.Printf("  кусков: %d → %d\n", res.ChunksBefore, res.ChunksAfter)
	fmt.Printf("  сегментов: %d → %d\n", res.SegmentsBefore, res.SegmentsAfter)
	fmt.Printf("  на диске: %.1f → %.1f МБ\n", float64(res.BytesBefore)/1e6, float64(res.BytesAfter)/1e6)
	return nil
}

// runKBEmbed считает смыслы коллекции без запуска интерфейса.
//
// Работа долгая, поэтому ей самое место под nohup. Ctrl+C безопасен:
// посчитанное сохранено, следующий запуск продолжит с того же места.
func runKBEmbed(cfg *config.Config, name string, dry bool) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	// Сервер эмбеддингов: свой из настроек, иначе первый сервер конфига.
	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	timeout := 5 * time.Minute
	emb := kbembed.New(cfg, fallback, timeout, nil)
	if emb == nil {
		return fmt.Errorf("смысловой поиск не настроен: задайте kb.embed_model в %s "+
			"(например \"bge-m3\") и вытяните модель: ollama pull bge-m3", cfg.Path)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opt := kb.EmbedOpts{Batch: cfg.KB.EmbedBatch, Workers: cfg.KB.EmbedWorkers}

	// Модель могла остаться в оперативной памяти после того, как карту занимала
	// чужая модель. Молча считать втрое дольше — плохая услуга.
	if w := emb.Placement(ctx).Warning(emb.Model()); w != "" {
		fmt.Fprintln(os.Stderr, "предупреждение: "+w)
	}

	if dry {
		res, err := coll.EstimateEmbed(ctx, emb, opt, 200)
		if err != nil {
			return err
		}
		if res.Added == 0 {
			fmt.Println("смыслы уже посчитаны для всей коллекции")
			return nil
		}
		fmt.Printf("коллекция %s, модель %s (%s)\n", name, emb.Model(), emb.URL())
		fmt.Printf("  осталось кусков: %d из %d\n", res.Added, res.Total)
		fmt.Printf("  размерность: %d\n", res.Dim)
		fmt.Printf("  ориентировочно: %s\n", res.Elapsed.Round(time.Second))
		fmt.Printf("  на диске ещё: %.0f МБ\n", float64(res.Bytes)/1e6)
		fmt.Println("оценка сделана замером на 200 кусках; посчитать: ollchat --kb-embed " + name)
		return nil
	}

	start := time.Now()
	res, err := coll.Embed(ctx, emb, opt, progressPrinter())
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	if res.Canceled {
		fmt.Printf("прервано: посчитано %d из %d кусков; продолжить — та же команда\n", res.Covered, res.Total)
		return nil
	}
	fmt.Printf("коллекция %s: смыслы посчитаны за %s\n", name, time.Since(start).Round(time.Second))
	fmt.Printf("  кусков: %d из %d, размерность %d, модель %s\n", res.Covered, res.Total, res.Dim, res.Model)
	fmt.Printf("  на диске: %.1f МБ\n", float64(res.Bytes)/1e6)
	return nil
}

// runKBList печатает состояние базы знаний.
func runKBList(cfg *config.Config) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	names, err := base.Names()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Printf("база знаний пуста: %s\nсобрать: ollchat --kb-index go /путь/к/книгам\n", cfg.KB.Dir)
		return nil
	}
	fmt.Printf("%-16s %7s %9s %8s %9s\n", "коллекция", "книг", "кусков", "сегм.", "размер")
	for _, n := range names {
		c, err := base.Open(n)
		if err != nil {
			fmt.Printf("%-16s  — %s\n", n, err)
			continue
		}
		st := c.Stats()
		fmt.Printf("%-16s %7d %9d %8d %8.1fМБ\n", n, st.Indexed, st.Chunks, st.Segments, float64(st.Bytes)/1e6)
	}
	return nil
}

// progressPrinter печатает ход работы одной обновляемой строкой.
func progressPrinter() func(kb.Progress) {
	return func(p kb.Progress) {
		if p.Done {
			return
		}
		line := p.Phase
		if p.DocsTotal > 0 {
			line += fmt.Sprintf(" %d/%d", p.DocsDone, p.DocsTotal)
		}
		if p.Chunks > 0 {
			line += fmt.Sprintf(" · кусков %d", p.Chunks)
		}
		if p.Current != "" {
			line += " · " + p.Current
		}
		if len([]rune(line)) > 100 {
			line = string([]rune(line)[:99]) + "…"
		}
		fmt.Fprintf(os.Stderr, "\r%-100s", line)
	}
}

// allowedPaths проверяет, что каталоги разрешены настройкой kb.roots.
//
// Тот же список, что и в приложении: книги лежат вне песочницы, и разрешение
// на них даёт только файл настроек. Безголовый режим здесь ничем не вольнее.
func allowedPaths(cfg *config.Config, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("не указано ни одного каталога с книгами")
	}
	if len(cfg.KB.Roots) == 0 {
		return nil, fmt.Errorf("в настройках не указано ни одного каталога с книгами;\n" +
			"добавьте их в раздел [kb], например:\n  roots = [\"/mnt/books\"]")
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(config.ExpandPath(p))
		if err != nil {
			return nil, err
		}
		ok := false
		for _, r := range cfg.KB.Roots {
			if within(r, abs) {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("каталог %s вне разрешённых kb.roots:\n  %s",
				abs, strings.Join(cfg.KB.Roots, "\n  "))
		}
		out = append(out, abs)
	}
	return out, nil
}

func within(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == path {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
