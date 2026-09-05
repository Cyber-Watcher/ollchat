package maint

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/find"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/graphex"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"
	"github.com/Cyber-Watcher/ollchat/internal/kbrerank"
)

// Сборка графа понятий без интерфейса.
//
// Работа идёт часами, поэтому запускать её надо так же, как счёт смыслов:
// отдельной командой под tmux или nohup, а не из чата. Прерывание безопасно —
// разобранные куски отмечены, и повторный запуск продолжит с того же места.

// Build собирает или доливает граф коллекции.
func Build(stdout io.Writer, cfg *config.Config, name, folder string, limit, workers int,
	allowModelChange, allowPromptChange, redoEmpty, linkNew bool, logPath, kind, note string) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	ex := graphex.New(cfg.Graph.ExtractOptions(), fallback, 10*time.Minute, nil)
	if ex == nil {
		return fmt.Errorf("извлечение не настроено: задайте graph.model в %s "+
			"(например \"glm-4.7-flash:q8_0\")", cfg.Path)
	}

	// Проверка до начала работы. Иначе при закрытом сервере (а на время ночных
	// прогонов Ollama на стенде слушает только localhost) человек несколько
	// минут смотрит на неподвижную строку хода вместо внятного отказа.
	if err := ex.Check(context.Background()); err != nil {
		return err
	}

	chunks := coll.ChunkCount()
	// Назначение и пометка проставляются только при создании: у графа, который
	// уже собран, паспорт менять нельзя — иначе рабочий однажды станет опытным
	// по опечатке в ключе, и доктор о нём замолчит.
	g, err := graph.OpenOrCreateKind(coll.Dir(), name, chunks, graph.Kind(kind), note, cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()

	filter := kb.ChunkFilter{PathContains: folder}
	books := coll.MatchingDocs(filter)
	inFolder := coll.CountChunks(filter)
	if inFolder == 0 {
		if folder != "" {
			return fmt.Errorf("в коллекции %s нет книг, чей путь содержит %q", name, folder)
		}
		return fmt.Errorf("в коллекции %s нет кусков", name)
	}

	// Признак сборки, оставшийся от упавшего прогона, снимается сам —
	// но молча этого делать нельзя: человек должен знать, что работа
	// подобрана за кем-то, а не начата с чистого листа.
	if s := g.StaleLock(); s != "" {
		fmt.Fprintf(stdout, "снят признак идущей сборки: %s\n", s)
	}
	fmt.Fprintf(stdout, "коллекция %s, модель извлечения %s\n", name, ex.Model())
	if folder != "" {
		fmt.Fprintf(stdout, "отбор по пути: %q — книг %d, кусков %d\n", folder, len(books), inFolder)
	} else {
		fmt.Fprintf(stdout, "вся коллекция: книг %d, кусков %d\n", len(books), inFolder)
	}
	if limit > 0 {
		fmt.Fprintf(stdout, "за этот заход: не больше %d кусков (замер скорости)\n", limit)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Связывание новых имён (этап 90, пункт 5): вектор — той же моделью, что
	// векторы понятий графа; арбитр — модель извлечения.
	var link *graph.LinkOpts
	if linkNew {
		emb := kbembed.New(cfg.KB.EmbedOptions(), fallback, 5*time.Minute, nil)
		if emb == nil {
			return fmt.Errorf("--graph-link-new требует модели эмбеддингов: задайте kb.embed_model")
		}
		if info := g.VectorsInfo(); !info.Ready {
			return fmt.Errorf("--graph-link-new: у графа нет векторов понятий — сперва ollchat --graph-embed %s", name)
		}
		link = &graph.LinkOpts{Embedder: emb, Judge: ex, MinCos: cfg.Graph.LinkMinCos}
		fmt.Fprintf(stdout, "связывание новых имён: порог близости %.2f, арбитр %s\n", link.MinCos, ex.Model())
	}

	start := time.Now()
	res, err := graph.Build(ctx, coll, g, ex, graph.BuildOpts{
		Link:              link,
		Folder:            folder,
		Limit:             limit,
		Workers:           workers,
		Retry:             cfg.Graph.Retry,
		AllowModelChange:  allowModelChange,
		AllowPromptChange: allowPromptChange,
		RedoEmpty:         redoEmpty,
	}, graphProgress(logPath))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}

	if res.Total == 0 {
		fmt.Fprintln(stdout, "всё уже разобрано — новых кусков нет")
		return nil
	}
	if res.Canceled {
		fmt.Fprintf(stdout, "прервано: разобрано %d из %d кусков; продолжить — та же команда\n",
			res.Done, res.Total)
	}
	elapsed := time.Since(start)
	fmt.Fprintf(stdout, "разобрано кусков: %d за %s (%.1f кусков/с)\n",
		res.Done, elapsed.Round(time.Second), res.Rate())
	fmt.Fprintf(stdout, "  пусто: %d, пропущено: %d\n", res.Empty, res.Skipped)
	fmt.Fprintf(stdout, "  сущностей: %d (+%d), связей: %d (+%d)\n",
		res.Entities, res.NewEntities, res.Edges, res.NewEdges)

	// Оценка остатка — то, ради чего и делается замер на малом числе кусков:
	// «три часа» и «трое суток» это разные решения. Остаток берётся из самой
	// сборки: она одна знает, сколько кусков этого отбора уже разобрано
	// прошлыми заходами.
	if remaining := res.Pending; remaining > 0 && res.Rate() > 0 {
		eta := time.Duration(float64(remaining)/res.Rate()) * time.Second
		fmt.Fprintf(stdout, "осталось кусков %d — это ещё около %s при той же скорости\n",
			remaining, eta.Round(time.Minute))
	}
	return nil
}

// Find ищет по графу из командной строки.
//
// Тот же поиск, что получит модель инструментом graph_search, только вход
// человеческий. Нужен не для красоты: графом должен уметь пользоваться и тот,
// кто его собрал, и сторонняя программа, и другой ассистент — а не только
// модель внутри ollchat.
// printOpen печатает, во что обошлось открытие графа.
//
// Обе величины растут вместе с библиотекой и однажды упрутся (порог назван
// в HowGraphBuildRuns.md). Заметить приближение можно, только если числа
// видны каждый день, поэтому строка печатается при каждом открытии — но
// приглушённо, пока всё в норме.
//
// Цвет здесь тот же, что в интерфейсе, и берётся из тех же настроек:
// graph.open_slow_seconds, graph.open_hot_seconds и три цвета к ним.
func printOpen(stdout io.Writer, g *graph.Graph, cfg *config.Config) {
	st := g.Opened()
	if st.Elapsed <= 0 {
		return
	}
	fmt.Fprintln(os.Stderr, OpenNote(st, &cfg.Graph))
}

// Find — поиск без модели из командной строки.
//
// **Это тот же поиск, что `/search` в интерфейсе, и намеренно тот же код.**
// До 02.09.2026 команда звала `graph.Search` напрямую и печатала только
// куски-подтверждения найденных понятий. Выглядело это как поиск по книгам —
// раздел так и назывался, — но им не было: ни BM25, ни векторов, ни слияния,
// ни `kb.max_per_book`, ни второй ступени. На вопросе, где граф цеплялся
// за неудачное понятие (`Knowledge base` с синонимом «внешний источник»),
// выдача выглядела сломанным поиском по книгам, хотя книги при этом искались
// прекрасно — просто не здесь.
//
// Расхождение стоило мне часа ложных выводов: я обвинил по очереди расширение
// запроса через граф и вторую ступень, а мерил не тот путь. Две команды,
// названные в README одним и тем же, обязаны быть одним и тем же кодом.
func Find(stdout io.Writer, cfg *config.Config, name, query string, asJSON bool) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	if name == "" {
		name = cfg.KB.Default
	}
	if name == "" {
		names, err := base.Names()
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return fmt.Errorf("в базе знаний нет коллекций")
		}
		name = names[0]
	}
	coll, err := base.Open(name)
	if err != nil {
		return graphNeedsLocalFiles(cfg, name, err)
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	if !asJSON {
		printOpen(stdout, g, cfg) // при --graph-json вывод обязан оставаться разбираемым
	}

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	deps := find.Deps{
		Coll:     coll,
		Graph:    g,
		Embedder: kbembed.New(cfg.KB.EmbedOptions(), fallback, 0, nil),
		Reranker: kbrerank.New(cfg.KB.RerankOptions()),
	}
	opts := find.Opts{
		Collection:     name,
		TopK:           cfg.KB.TopK,
		MaxPerBook:     cfg.KB.MaxPerBook,
		MinCosine:      cfg.KB.MinCosine,
		SemanticWeight: cfg.KB.SemanticWeight,
		Semantic:       cfg.KB.Semantic,
		Rerank:         deps.Reranker != nil,
		Entities:       cfg.Mix.Entities,
		Neighbors:      cfg.Mix.Neighbors,
		// Ранжирование связей по вопросу, а не только по весу. Без него наверх
		// лезут самые частые связи понятия: у `Go` это `fmt —часть→ Go`
		// с 474 подтверждениями, к вопросу о сборщике мусора не относящееся.
		Rank:         NeighborRank(cfg),
		QueryTimeout: cfg.KB.QueryTimeoutDuration(),
		RerankOpts: kb.RerankOpts{
			Candidates: cfg.KB.RerankCandidates,
			Snippet:    cfg.KB.RerankSnippet,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := find.Search(ctx, deps, query, opts)
	if err != nil {
		return err
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Fprint(stdout, find.Render(res, false, coll))
	return nil
}

func graphNeedsLocalFiles(cfg *config.Config, name string, err error) error {
	if strings.TrimSpace(cfg.KB.ServerURL) == "" {
		return err // обычная работа с файлами — отказ и так по существу
	}
	return fmt.Errorf("граф понятий через общую библиотеку (%s) пока не раздаётся, "+
		"а локальной коллекции %q в каталоге %s нет.\n"+
		"Поиск по книгам при этом работает: спрашивайте модель или /kb search.\n"+
		"Для поиска по графу нужна локальная копия коллекции вместе с каталогом graph/",
		cfg.KB.ServerURL, name, cfg.KB.Dir)
}

// NeighborRank собирает настройки ранжирования связей из конфига.
//
// Одно место на всю программу: и поиск из командной строки, и инструменты
// модели обязаны вести себя одинаково, иначе замер на одном не относится
// к другому.
// CacheFor заводит кэш открытых графов, если он не выключен настройкой.
//
// nil означает «открывать на каждый вызов» — инструменты это понимают.
// Выключение осмысленно там, где мало памяти: открытие стоит 11.5 с и до 1.03 ГБ
// пика, зато после него граф удерживает всего 160 МБ.
func CacheFor(cfg *config.Config, ttl time.Duration) *graph.Cache {
	if !cfg.Graph.Cache {
		return nil
	}
	return graph.NewCache(ttl, cfg.Graph.Rules())
}

func NeighborRank(cfg *config.Config) graph.NeighborRank {
	return graph.NeighborRank{
		SenseWeight: cfg.Graph.NeighborSenseWeight,
		Pool:        cfg.Graph.NeighborPool,
	}
}

func QueryVector(g *graph.Graph, cfg *config.Config, query string) []int8 {
	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	emb := kbembed.New(cfg.KB.EmbedOptions(), fallback, 30*time.Second, nil)
	if emb == nil {
		return nil
	}
	// Тем же ядром, что /search и инструменты (этап 91, R2.5).
	qv, _, _ := find.QueryVector(context.Background(), find.Deps{Graph: g, Embedder: emb}, query,
		find.Opts{Semantic: true, QueryTimeout: 30 * time.Second})
	return qv
}

// graphProgress печатает ход работы одной перезаписываемой строкой.
// graphProgress печатает ход сборки в терминал, а при заданном пути — ещё
// и в файл.
//
// Зачем файл, если есть перенаправление вывода. Затем, что перенаправлением
// распоряжается не программа. Замер этих суток: `sed`, `awk` и `tail` копят
// вывод блоками, когда пишут не в терминал, и ход сборки, пропущенный через
// `tail -3`, не появлялся в журнале до конца каталога — то есть часами.
// Ключ убирает целый класс таких потерь: программа пишет туда, куда велено.
//
// В файл идут полные строки с отметкой времени, а не возврат каретки: `\r`
// хорош для живого терминала и бесполезен в журнале, где нужна история.
func graphProgress(logPath string) func(graph.BuildProgress) {
	var last time.Time
	var logFile *os.File
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "журнал хода %s не открылся: %v\n", logPath, err)
		} else {
			logFile = f
		}
	}
	return func(p graph.BuildProgress) {
		// Не чаще раза в секунду: сборка идёт часами, и поток строк
		// в журнале nohup вырос бы до сотен мегабайт.
		if time.Since(last) < time.Second && p.Done < p.Total {
			return
		}
		last = time.Now()
		book := p.Book
		if len([]rune(book)) > 40 {
			book = string([]rune(book)[:39]) + "…"
		}
		rest := ""
		if p.Rate() > 0 && p.Total > p.Done {
			eta := time.Duration(float64(p.Total-p.Done)/p.Rate()) * time.Second
			rest = fmt.Sprintf(", ещё ~%s", eta.Round(time.Minute))
		}
		fmt.Fprintf(os.Stderr, "\r\033[K%d/%d кусков · %.1f/с · понятий %d · связей %d%s · %s",
			p.Done, p.Total, p.Rate(), p.Entities, p.Edges, rest, book)
		if logFile != nil {
			fmt.Fprintf(logFile, "%s %d/%d кусков · %.1f/с · понятий %d · связей %d%s · %s\n",
				time.Now().Format("15:04:05"),
				p.Done, p.Total, p.Rate(), p.Entities, p.Edges, rest, book)
		}
	}
}

// Status печатает состояние графа коллекции.
// Status печатает состояние графа, а при заданном каталоге — ещё
// и по одному каталогу отдельно.
//
// Разбивка по каталогу нужна, чтобы отвечать на «сколько осталось» числом,
// а не оценкой. Сама сборка это знает, но её вывод легко потерять, а перезапуск
// ради ответа невозможен: замок не пустит. Замер 27.08.2026 показал, зачем это:
// я оценивал каталог по среднему числу кусков на книгу (1 240) и ошибся вдвое —
// в книгах по информационной безопасности оказалось 1 750 на книгу, и срок
// вырос с четырёх часов до восьми.
func Status(stdout io.Writer, cfg *config.Config, name, folder string) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	names := []string{name}
	if name == "" {
		if names, err = base.Names(); err != nil {
			return err
		}
	}
	for _, n := range names {
		coll, err := base.Open(n)
		if err != nil {
			return err
		}
		chunks := coll.ChunkCount()
		g, err := graph.Open(coll.Dir(), chunks, cfg.Graph.Rules())
		if err != nil {
			fmt.Fprintf(stdout, "%s: %v\n", n, err)
			continue
		}
		printOpen(stdout, g, cfg)
		st := g.Stats(chunks)
		fmt.Fprintf(stdout, "%s: понятий %d, связей %d, упоминаний %d\n",
			n, st.Entities, st.Edges, st.Mentions)
		done, empty, skipped := g.Progress().Counts()
		fmt.Fprintf(stdout, "  разобрано кусков %d из %d (осталось %d)\n", st.Covered, chunks, st.Pending)
		fmt.Fprintf(stdout, "  из них с понятиями %d, пустых %d, пропущено %d\n", done, empty, skipped)
		if st.Model != "" {
			fmt.Fprintf(stdout, "  модель извлечения: %s\n", st.Model)
		}
		fmt.Fprintf(stdout, "  %s\n", g.PromptLine())

		// Смыслы устаревают молча: поиск не находит того, чего нет в векторах,
		// и не жалуется. Показываем расхождение сами — заметить его иначе
		// можно только сравнив два числа в разных файлах.
		if info := g.VectorsInfo(); info.Ready {
			if info.Count >= st.Entities {
				fmt.Fprintf(stdout, "  векторы(смыслы) понятий: посчитаны все %d (%s)\n", info.Count, info.Model)
			} else {
				fmt.Fprintf(stdout, "  векторы(смыслы) понятий: посчитано %d из %d — %d не находятся "+
					"по смыслу (ollchat --graph-embed %s)\n",
					info.Count, st.Entities, st.Entities-info.Count, n)
			}
		} else if st.Entities > 0 {
			fmt.Fprintf(stdout, "  векторы(смыслы) понятий не считались (ollchat --graph-embed %s)\n", n)
		}
		if folder != "" {
			var total, covered int
			err := coll.EachChunkRef(kb.ChunkFilter{PathContains: folder}, func(r kb.ChunkRef) error {
				total++
				if g.Progress().Done(graph.ChunkKey{Doc: r.Doc, Ord: r.Ord}) {
					covered++
				}
				return nil
			})
			switch {
			case err != nil:
				fmt.Fprintf(stdout, "  каталог %s: %v\n", folder, err)
			case total == 0:
				fmt.Fprintf(stdout, "  каталог %s: кусков не нашлось — проверьте написание пути\n", folder)
			default:
				fmt.Fprintf(stdout, "  каталог %s: кусков %d, разобрано %d, осталось %d (%d%%)\n",
					folder, total, covered, total-covered, 100*covered/total)
			}
		}
		if g.Locked() {
			fmt.Fprintln(stdout, "  идёт сборка")
		}
		g.Close()
	}
	return nil
}

// graphFolderHint подсказывает, какие каталоги есть в коллекции.
func graphFolderHint(coll *kb.Collection) string {
	fs := coll.Breakdown()
	if len(fs) < 2 {
		return ""
	}
	var names []string
	for i, f := range fs {
		if i >= 8 {
			break
		}
		names = append(names, f.Folder)
	}
	return "каталоги коллекции: " + strings.Join(names, ", ")
}

// Communities размечает граф на сообщества.
//
// Модель здесь не участвует: это арифметика по весам связей, секунды работы
// процессора. Резюме сообществ пишет модель, но отдельным шагом — иначе
// разбиение нельзя было бы перестроить, не занимая карту.
func Communities(stdout io.Writer, cfg *config.Config, name string, fresh bool, similarity float64) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	unmark, err := markWork(g, "разметка тем")
	if err != nil {
		return err
	}
	defer unmark()

	start := time.Now()
	res, err := g.BuildCommunitiesWith(graph.CommunityOpts{
		Resolution:      cfg.Graph.Resolution,
		MaxSize:         cfg.Graph.MaxCommunity,
		MaxDepth:        cfg.Graph.SplitDepth,
		Fresh:           fresh,
		CarrySimilarity: similarity,
	})
	if err != nil {
		return err
	}

	small, big := res.Level(0), res.Level(1)
	fmt.Fprintf(stdout, "коллекция %s: понятий %d, связей %d\n", name, res.Entities, res.Edges)
	fmt.Fprintf(stdout, "сообществ: %d мелких, %d объединений, за %s\n",
		len(small), len(big), time.Since(start).Round(time.Millisecond))

	// Перенос описаний — главное, что отличает пересчёт от пересборки с нуля.
	// Без него каждый пересчёт стоил бы полутора часов работы карты заново.
	if c := res.Carry; c.Total > 0 {
		if fresh {
			fmt.Fprintf(stdout, "описания не переносились (--graph-communities-fresh): "+
				"заново предстоит описать %d тем\n", c.Total)
		} else {
			fmt.Fprintf(stdout, "описания перенесены у %d тем из %d, без описания %d, "+
				"потеряно %d\n", c.Carried, c.Total, c.Fresh, c.Lost)
			if c.Carried > 0 {
				// Замеры: 2 590 резюме — 35 минут, 337 разборов — 22 минуты.
				// Считаем по отдельности: разбор дороже и есть не у всех тем.
				saved := float64(c.Carried)*35.0/2590.0 + float64(c.CarriedFindings)*22.0/337.0
				fmt.Fprintf(stdout, "  из них с готовым разбором %d; сэкономлено примерно %.0f мин работы карты\n",
					c.CarriedFindings, saved)
			}
		}
	}

	// Размеры важнее числа: сотня сообществ по два понятия — это не разбиение,
	// а список, и резюме по ним писать бессмысленно.
	sizes := make([]int, 0, len(small))
	for _, c := range small {
		sizes = append(sizes, len(c.Members))
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	one := 0
	for _, n := range sizes {
		if n == 1 {
			one++
		}
	}
	if len(sizes) > 0 {
		fmt.Fprintf(stdout, "  размеры: наибольшее %d, медиана %d, из одного понятия %d\n",
			sizes[0], sizes[len(sizes)/2], one)
	}

	fmt.Fprintln(stdout, "\nсамые крупные сообщества:")
	sort.Slice(small, func(i, j int) bool { return len(small[i].Members) > len(small[j].Members) })
	for i, c := range small {
		if i >= 8 {
			break
		}
		var names []string
		for _, id := range c.Members {
			if e, ok := g.Entities().Get(id); ok {
				names = append(names, e.Name)
			}
			if len(names) >= 6 {
				break
			}
		}
		fmt.Fprintf(stdout, "  #%-4d понятий %-5d вес %-8.0f %s\n",
			c.ID, len(c.Members), c.Weight, strings.Join(names, ", "))
	}
	return nil
}

// Recheck передоописывает самые рыхлые темы моделью извлечения.
//
// Быстрая модель, которой пишутся резюме, охотно придумывает связное название
// бессвязному набору понятий и ставит ему высокую оценку — а обзор тем
// отбирает темы как раз по оценке, и пустышка попадает в него как настоящая.
// Ловить такие наборы по одной оценке нельзя: модель, которая их сочинила,
// сама себя и не заподозрит.
//
// Поэтому подозреваемых выбирает не модель, а устройство графа: у бессвязного
// набора связи чаще уходят наружу, чем остаются внутри (graph.LowCohesion).
// Их немного — десятки против тысяч, — и описать их заново честной моделью
// стоит минуты. Ложные срабатывания безвредны: настоящую тему модель опишет
// так же хорошо, просто медленнее.
func Recheck(stdout io.Writer, cfg *config.Config, name string, count, minMembers int) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	unmark, err := markWork(g, "передоописание тем")
	if err != nil {
		return err
	}
	defer unmark()

	comms, err := g.LoadCommunities()
	if err != nil {
		return err
	}
	if comms == nil {
		return fmt.Errorf("сообщества не размечены: сперва ollchat --graph-communities %s", name)
	}
	if count <= 0 {
		count = 50
	}
	if minMembers <= 0 {
		minMembers = 20
	}

	weak := g.LowCohesion(comms, minMembers, count)
	if len(weak) == 0 {
		fmt.Fprintln(stdout, "тем крупнее", minMembers, "понятий со связями не нашлось — проверять нечего")
		return nil
	}

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	// Нарочно без WithModel: проверяет именно модель извлечения, та самая,
	// ради честности которой всё и затевается.
	ex := graphex.New(cfg.Graph.ExtractOptions(), fallback, 10*time.Minute, nil)
	if ex == nil {
		return fmt.Errorf("извлечение не настроено: задайте graph.model в %s", cfg.Path)
	}
	if err := ex.Check(context.Background()); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "коллекция %s, проверяет %s, тем на пересмотр %d (связность %.2f–%.2f)\n",
		name, ex.Model(), len(weak), weak[0].Share, weak[len(weak)-1].Share)

	// Что было — показать рядом с тем, что стало: иначе непонятно, что дала
	// проверка и стоит ли её вообще запускать.
	before := make(map[int]graph.Community, len(weak))
	for _, w := range weak {
		if com, ok := comms.Get(w.ID); ok {
			before[w.ID] = com
		}
		comms.ForgetSummary(w.ID)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	last := time.Now()
	err = g.Summarize(ctx, ex, comms, graph.SummaryOpts{
		MinMembers:   minMembers,
		MaxMembers:   cfg.Graph.SummaryMaxMembers,
		MaxRelations: cfg.Graph.SummaryMaxRelations,
		Workers:      cfg.Graph.SummaryWorkers,
	},
		func(p graph.SummaryProgress) {
			if time.Since(last) < 2*time.Second && p.Done+p.Failed < p.Total {
				return
			}
			last = time.Now()
			fmt.Fprintf(os.Stderr, "\r  %d/%d · сбоев %d · %s   ",
				p.Done+p.Failed, p.Total, p.Failed, trimTitle(p.Title))
		})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}

	dropped := 0
	for _, w := range weak {
		now, ok := comms.Get(w.ID)
		if !ok || now.Title == "" {
			continue
		}
		was := before[w.ID]
		if now.Rating < was.Rating {
			dropped++
		}
		mark := " "
		if was.Rating-now.Rating >= 3 {
			mark = "!"
		}
		fmt.Fprintf(stdout, "%s #%-5d связность %.2f  оценка %2d → %2d  %s\n",
			mark, w.ID, w.Share, was.Rating, now.Rating, trimTitle(now.Title))
	}
	fmt.Fprintf(stdout, "\nпересмотрено %d тем, оценка снижена у %d\n", len(weak), dropped)
	return nil
}

// Summaries просит модель назвать и описать каждое сообщество.
//
// В отличие от сборки графа, работа тут короткая: один запрос на сообщество,
// сотни запросов вместо сотен тысяч. Но карту она всё равно занимает, поэтому
// запускается человеком отдельной командой, а не сама после разбиения.
func Summaries(stdout io.Writer, cfg *config.Config, name string, minMembers int) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	unmark, err := markWork(g, "описания тем")
	if err != nil {
		return err
	}
	defer unmark()

	comms, err := g.LoadCommunities()
	if err != nil {
		return err
	}
	if comms == nil {
		return fmt.Errorf("сообщества не размечены: сперва ollchat --graph-communities %s", name)
	}

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	ex := graphex.New(cfg.Graph.ExtractOptions(), fallback, 10*time.Minute, nil)
	if ex == nil {
		return fmt.Errorf("извлечение не настроено: задайте graph.model в %s", cfg.Path)
	}
	// Описание тем может идти не той моделью, что извлечение: см. WithModel.
	ex = ex.WithModel(cfg.Graph.SummaryModel, cfg.Graph.SummaryWorkers)
	if err := ex.Check(context.Background()); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(stdout, "коллекция %s, модель %s, сообществ всего %d\n", name, ex.Model(), len(comms.List))
	last := time.Now()
	if minMembers <= 0 {
		minMembers = cfg.Graph.SummaryMinMembers
	}
	err = g.Summarize(ctx, ex, comms, graph.SummaryOpts{
		MinMembers:   minMembers,
		MaxMembers:   cfg.Graph.SummaryMaxMembers,
		MaxRelations: cfg.Graph.SummaryMaxRelations,
		Workers:      cfg.Graph.SummaryWorkers,
	},
		func(p graph.SummaryProgress) {
			if time.Since(last) < 2*time.Second && p.Done+p.Failed < p.Total {
				return
			}
			last = time.Now()
			fmt.Fprintf(os.Stderr, "\r  %d/%d · сбоев %d · %s   ",
				p.Done+p.Failed, p.Total, p.Failed, trimTitle(p.Title))
		})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}

	done := 0
	for _, c := range comms.List {
		if c.Title != "" {
			done++
		}
	}
	fmt.Fprintf(stdout, "описано сообществ: %d\n", done)
	return nil
}

// trimTitle укорачивает заголовок для строки хода.
func trimTitle(s string) string {
	r := []rune(s)
	if len(r) > 44 {
		return string(r[:44]) + "…"
	}
	return s
}

// Embed считает векторы понятий графа — смысловой вход.
//
// Отдельной командой, а не сама после сборки: карту она занимает, пусть
// и на минуты, а решение занимать карту принимает человек. После каждой
// докатки графа векторы устаревают — новые понятия остаются без них,
// и команда об этом говорит числами.
func Embed(stdout io.Writer, cfg *config.Config, name string, recount bool) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	unmark, err := markWork(g, "векторы понятий")
	if err != nil {
		return err
	}
	defer unmark()

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	emb := kbembed.New(cfg.KB.EmbedOptions(), fallback, 5*time.Minute, nil)
	if emb == nil {
		return fmt.Errorf("смысловой поиск не настроен: задайте kb.embed_model в %s", cfg.Path)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st := g.Stats(coll.ChunkCount())
	prev := g.VectorsInfo()
	fmt.Fprintf(stdout, "коллекция %s, понятий %d, модель %s\n", name, st.Entities, emb.Model())
	switch {
	case recount:
		fmt.Fprintln(stdout, "пересчёт всех векторов заново")
	case prev.Ready && prev.Model == emb.Model():
		fmt.Fprintf(stdout, "уже посчитано %d, досчитываем %d новых\n",
			prev.Count, max(st.Entities-prev.Count, 0))
	}
	last := time.Now()
	err = g.EmbedEntities(ctx, emb, graph.EmbedOpts{
		Batch:   cfg.KB.EmbedBatch,
		Workers: cfg.KB.EmbedWorkers,
		Recount: recount,
	}, func(p graph.EmbedProgress) {
		if time.Since(last) < time.Second && p.Done < p.Total {
			return
		}
		last = time.Now()
		fmt.Fprintf(os.Stderr, "\r  %d/%d понятий   ", p.Done, p.Total)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	info := g.VectorsInfo()
	fmt.Fprintf(stdout, "посчитано: понятий %d, размерность %d, модель %s\n",
		info.Count, info.Dim, info.Model)
	return nil
}

// Findings пишет разбор по важным темам — пятый раздел отчёта.
//
// Отдельная команда и отдельный отбор, потому что работа стоит времени карты:
// выводы дороже резюме в разы, и делать их для всех тем ради обзора, который
// их даже не показывает, было бы тратой. Отбираются крупные темы с высокой
// оценкой; сколько их — команда говорит до начала работы.
func Findings(stdout io.Writer, cfg *config.Config, name string, minRating, minMembers int, redo, dry bool) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	unmark, err := markWork(g, "разборы тем")
	if err != nil {
		return err
	}
	defer unmark()

	comms, err := g.LoadCommunities()
	if err != nil {
		return err
	}
	if comms == nil {
		return fmt.Errorf("сообщества не размечены: сперва ollchat --graph-communities %s", name)
	}

	opt := graph.FindingsOpts{
		MinRating:    minRating,
		MinMembers:   minMembers,
		MaxMembers:   cfg.Graph.SummaryMaxMembers,
		MaxRelations: cfg.Graph.SummaryMaxRelations,
		Workers:      cfg.Graph.SummaryWorkers,
		Redo:         redo,
	}
	work := comms.SelectForFindings(opt)
	fmt.Fprintf(stdout, "коллекция %s, тем всего %d, под разбор подходит %d\n",
		name, len(comms.List), len(work))
	if dry || len(work) == 0 {
		return nil
	}

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	ex := graphex.New(cfg.Graph.ExtractOptions(), fallback, 10*time.Minute, nil)
	if ex == nil {
		return fmt.Errorf("извлечение не настроено: задайте graph.model в %s", cfg.Path)
	}
	// Выводы — обычный связный текст по-русски, синонимы для него не нужны:
	// годится та же быстрая модель, что пишет резюме.
	ex = ex.WithModel(cfg.Graph.SummaryModel, cfg.Graph.SummaryWorkers)
	if err := ex.Check(context.Background()); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(stdout, "разбирает %s\n", ex.Model())
	last := time.Now()
	err = g.Findings(ctx, ex, comms, opt, func(p graph.FindingsProgress) {
		if time.Since(last) < 2*time.Second && p.Done+p.Failed < p.Total {
			return
		}
		last = time.Now()
		fmt.Fprintf(os.Stderr, "\r  %d/%d · сбоев %d · %s   ",
			p.Done+p.Failed, p.Total, p.Failed, trimTitle(p.Title))
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}

	withFindings, total := 0, 0
	for _, c := range comms.List {
		if len(c.Findings) > 0 {
			withFindings++
			total += len(c.Findings)
		}
	}
	fmt.Fprintf(stdout, "тем с разбором %d, выводов всего %d (в среднем %.1f на тему)\n",
		withFindings, total, float64(total)/float64(max(withFindings, 1)))
	return nil
}

// Drift отвечает на вопрос «пора ли пересчитывать сообщества».
//
// Отвечает прямо, а не косвенно: строит новое разбиение в памяти и сравнивает
// с нынешним. Косвенные признаки вроде «граф вырос на столько-то» тут негодны —
// рост в стороне от размеченных тем не меняет ничего, а рост внутри них
// перекраивает всё.
//
// Ничего не сохраняет: команда обязана быть безобидной, иначе ею перестанут
// пользоваться из осторожности.
func Drift(stdout io.Writer, cfg *config.Config, name string, similarity float64, show int) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()

	comms, err := g.LoadCommunities()
	if err != nil {
		return err
	}
	if comms == nil {
		fmt.Fprintln(stdout, "сообщества ещё не размечены: ollchat --graph-communities", name)
		return nil
	}

	opt := graph.CommunityOpts{
		MaxSize:    cfg.Graph.MaxCommunity,
		MaxDepth:   cfg.Graph.SplitDepth,
		Resolution: cfg.Graph.Resolution,
	}
	d, err := g.DriftOf(comms, opt, similarity)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "коллекция %s\n", name)
	fmt.Fprintf(stdout, "  понятий сейчас %d, на момент разбиения %d (+%d)\n",
		d.Entities, d.EntitiesThen, d.Entities-d.EntitiesThen)
	fmt.Fprintf(stdout, "  связей сейчас %d, на момент разбиения %d (+%d)\n",
		d.Edges, d.EdgesThen, d.Edges-d.EdgesThen)
	fmt.Fprintf(stdout, "  понятий вне размеченных тем: %d\n", d.Uncovered)
	fmt.Fprintf(stdout, "\n  описанных тем: %d\n", d.Themes)
	fmt.Fprintf(stdout, "  осталось бы почти как есть: %d (%.0f%%)\n", d.Kept, 100*d.Ratio())
	fmt.Fprintf(stdout, "  перекроилось бы: %d\n", d.Changed)
	fmt.Fprintf(stdout, "  (темой «почти как есть» считается совпадение состава от %.0f%%)\n",
		100*d.Similarity)
	fmt.Fprintf(stdout, "\n%s\n", d.Verdict())

	// Цена решения — она и удерживает от пересчёта по каждой книге.
	if d.Themes > 0 {
		fmt.Fprintf(stdout, "\nпересчёт сотрёт описания у всех %d тем: заново это резюме "+
			"(~35 мин карты), разборы (~22 мин) и векторы понятий (~20 мин)\n", d.Themes)
	}

	if show > 0 {
		topics, err := g.DriftTopics(comms, opt, show)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "\nсильнее всего перекроились бы:\n")
		for _, t := range topics {
			fmt.Fprintf(stdout, "  совпадение %.0f%% · понятий %d · разошлись бы на %d %s · %s\n",
				100*t.Similarity, t.Members, t.Pieces, pieces(t.Pieces), trimTitle(t.Title))
		}
	}
	return nil
}

// pieces склоняет слово «часть» по числу: «на 1 частей» читается как небрежность,
// а небрежность в выводе заставляет сомневаться и в числах.
func pieces(n int) string {
	switch {
	case n%10 == 1 && n%100 != 11:
		return "часть"
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		return "части"
	default:
		return "частей"
	}
}

// Tune подбирает разрешение разбиения.
//
// Считает на процессоре и ничего не сохраняет: перебрать десяток значений
// дешевле, чем рассуждать об одном.
func Tune(stdout io.Writer, cfg *config.Config, name, list string, samples int) error {
	var resolutions []float64
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseFloat(part, 64)
		if err != nil || v <= 0 {
			return fmt.Errorf("разрешение %q не число больше нуля", part)
		}
		resolutions = append(resolutions, v)
	}
	if len(resolutions) == 0 {
		// Вокруг нынешнего значения: подбор почти всегда начинается с вопроса
		// «а не сдвинуть ли то, что стоит».
		resolutions = []float64{1, 3, 5, 8}
	}

	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()

	opt := graph.CommunityOpts{
		MaxSize:  cfg.Graph.MaxCommunity,
		MaxDepth: cfg.Graph.SplitDepth,
	}
	rows, err := g.Tune(opt, resolutions, samples, 6)
	if err != nil {
		return err
	}

	// Ноль в настройках означает «как в пакете», и показывать его как ноль
	// значит прятать от человека то, с чем он сравнивает.
	cur := cfg.Graph.Resolution
	if cur <= 0 {
		cur = graph.DefaultResolution
	}
	fmt.Fprintf(stdout, "коллекция %s, понятий %d, связей %d\n\n",
		name, g.Entities().Count(), g.Edges().Count())
	fmt.Fprintf(stdout, "%6s %8s %10s %9s %11s %10s %10s\n",
		"γ", "тем", "крупнейшая", "срединная", "крупнее нормы", "из одной", "связность")
	fmt.Fprintln(stdout, "  "+dashes(70))
	for _, r := range rows {
		mark := " "
		if cur > 0 && r.Resolution == cur {
			mark = "*" // то, что стоит в настройках сейчас
		}
		fmt.Fprintf(stdout, "%s%5.1f %8d %10d %9d %11d %10d %10.2f\n",
			mark, r.Resolution, r.Topics, r.Largest, r.Median, r.Oversized, r.Singletons, r.Cohesion)
	}
	fmt.Fprintf(stdout, "\n  * — то, что действует сейчас (γ = %.1f)\n", cur)

	// Числа не отличают связную тему от свалки одинакового размера, поэтому
	// крупнейшие темы показываются составом.
	if samples > 0 {
		for _, r := range rows {
			fmt.Fprintf(stdout, "\nγ = %.1f, крупнейшие темы:\n", r.Resolution)
			for _, s := range r.Samples {
				fmt.Fprintf(stdout, "  понятий %-5d %s\n", s.Members, strings.Join(s.Names, ", "))
			}
		}
	}
	fmt.Fprintln(stdout, "\nразбиение считалось в памяти, на диске ничего не изменилось")
	return nil
}

// Bench сравнивает модели извлечения на одних и тех же кусках.
//
// Настоящий граф не трогается: каждая модель пишет в свой пустой граф
// во временном каталоге. Иначе замер портил бы то, что меряет.
func Bench(stdout io.Writer, cfg *config.Config, name, models, folder string,
	limit, workers int, keep string) error {

	var list []string
	for _, m := range strings.Split(models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			list = append(list, m)
		}
	}
	if len(list) < 2 {
		return fmt.Errorf("нужно хотя бы две модели через запятую: --graph-bench-models \"a,b\"")
	}

	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}

	fallback := ""
	if len(cfg.Servers) > 0 {
		fallback = cfg.Servers[0].URL
	}
	proto := graphex.New(cfg.Graph.ExtractOptions(), fallback, 10*time.Minute, nil)
	if proto == nil {
		return fmt.Errorf("извлечение не настроено: задайте graph.model в %s", cfg.Path)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opt := graph.BenchOpts{
		Folder:  folder,
		Limit:   limit,
		Workers: workers,
		Retry:   cfg.Graph.Retry,
		Keep:    keep,
	}
	shown := limit
	if shown <= 0 {
		shown = 50 // столько же подставит BenchOpts.norm
	}
	fmt.Fprintf(stdout, "коллекция %s, каталог %s, кусков на модель %d\n",
		name, orAll(folder), shown)
	fmt.Fprintf(stdout, "сравниваются: %s\n\n", strings.Join(list, ", "))

	results := make([]graph.BenchResult, 0, len(list))
	for _, m := range list {
		ex := proto.WithModel(m, workers)
		if err := ex.Check(ctx); err != nil {
			fmt.Fprintf(stdout, "── %s — пропущена: %v\n", m, err)
			results = append(results, graph.BenchResult{Model: m, Err: err})
			continue
		}
		fmt.Fprintf(stdout, "── %s\n", m)
		last := time.Now()
		r, err := graph.BenchModel(ctx, coll, ex, opt, func(p graph.BuildProgress) {
			if time.Since(last) < 2*time.Second && p.Done < p.Total {
				return
			}
			last = time.Now()
			fmt.Fprintf(os.Stderr, "\r   %d/%d кусков · %.2f/с   ", p.Done, p.Total, p.Rate())
		})
		fmt.Fprintln(os.Stderr)
		if err != nil && r.Err == nil {
			r.Err = err
		}
		results = append(results, r)
		if ctx.Err() != nil {
			break
		}
	}

	fmt.Fprintf(stdout, "\n%-24s %8s %10s %9s %9s %9s %8s\n",
		"модель", "кусков/с", "понятий/кус", "синонимы", "мост RU", "пусто", "сорвано")
	fmt.Fprintln(stdout, "  "+dashes(82))
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(stdout, "%-24s ошибка: %v\n", r.Model, r.Err)
			continue
		}
		fmt.Fprintf(stdout, "%-24s %8.2f %10.1f %8.0f%% %8.0f%% %9d %8d\n",
			r.Model, r.Rate(), r.PerChunk(), 100*r.AliasShare(), 100*r.BridgeShare(),
			r.Empty, r.Skipped)
	}
	fmt.Fprintln(stdout, `
  синонимы — доля понятий, у которых есть хоть одно другое написание
  мост RU  — доля АНГЛИЙСКИХ понятий, у которых синоним по-русски: именно он
             связывает русский вопрос с английской книгой
  сорвано  — ответов, которые не разобрались; вдвое быстрее при каждом пятом
             сорванном — это медленнее`)
	if keep != "" {
		fmt.Fprintf(stdout, "\nвременные графы сложены в %s\n", keep)
	} else {
		fmt.Fprintln(stdout, "\nвременные графы удалены, настоящий граф не тронут")
	}
	return nil
}

// orAll подставляет «вся коллекция», когда каталог не задан: пустая строка
// в отчёте читается как потерянное значение.
func orAll(folder string) string {
	if strings.TrimSpace(folder) == "" {
		return "вся коллекция"
	}
	return folder
}

// Resolve показывает двойников среди понятий графа.
//
// **Ничего не меняет.** Ни реестра сущностей, ни связей, ни сообществ: задача
// команды — дать человеку посмотреть на кандидатов и на признаки, по которым
// потом будет выбрано правило склейки. Правило, придуманное до того, как
// посмотрели на данные, один раз уже оказалось негодным (общие соседи).
func Resolve(stdout io.Writer, cfg *config.Config, name string, minCos, minCosMutual float64,
	full, crossOnly bool, show int, out string) error {

	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()

	if show == 0 {
		show = 40
	}
	if full {
		fmt.Fprintln(stdout, "полный перебор пар: работа идёт на процессоре и занимает минуты…")
	}
	pairs, st, err := g.ResolveCandidates(graph.ResolveOpts{
		MinCos: minCos, MinCosMutual: minCosMutual, Full: full, CrossOnly: crossOnly,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "коллекция %s\n", name)
	fmt.Fprintf(stdout, "  живых понятий %d", st.Entities)
	if st.Merged > 0 {
		fmt.Fprintf(stdout, " (поглощено склейкой %d)", st.Merged)
	}
	// Векторы считаются по записям реестра, а не по живым понятиям: место
	// вектора определяется номером, и поглощённые свои места сохраняют.
	registry := st.Entities + st.Merged
	fmt.Fprintf(stdout, ", с вектором %d из %d записей реестра\n", st.WithVectors, registry)
	if st.WithVectors < registry {
		fmt.Fprintf(stdout, "  без вектора %d — они в разбор не попали: ollchat --graph-embed %s\n",
			registry-st.WithVectors, name)
	}
	fmt.Fprintf(stdout, "  пар, связанных синонимом от модели: %d\n", st.AliasPairs)
	fmt.Fprintf(stdout, "  кандидатов после отбора: %d (за %s)\n\n", st.Found, st.Elapsed.Round(time.Millisecond))

	if len(pairs) == 0 {
		fmt.Fprintln(stdout, "подходящих пар нет")
		return nil
	}

	if out != "" {
		if err := writeResolveTSV(g, pairs, out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "все %d пар выписаны в %s\n\n", len(pairs), out)
	}

	fmt.Fprintln(stdout, "  близость  признаки           общих  книги    понятия")
	for i, p := range pairs {
		if i >= show {
			fmt.Fprintf(stdout, "\n  …и ещё %d пар", len(pairs)-show)
			if out == "" {
				fmt.Fprint(stdout, " (все — ключом --graph-resolve-out файл.tsv)")
			}
			fmt.Fprintln(stdout)
			break
		}
		a, _ := g.Entities().Get(p.A)
		b, _ := g.Entities().Get(p.B)
		arrow := "→"
		if p.Keep == p.A {
			arrow = "←"
		}
		fmt.Fprintf(stdout, "  %8s  %-18s %5s  %2d/%-2d   %s %s %s\n",
			fmt.Sprintf("%.3f", p.Cos), resolveMarks(p), sharedText(p),
			p.BooksA, p.BooksB, trimName(a.Name, 34), arrow, trimName(b.Name, 34))
	}

	fmt.Fprintln(stdout, "\n  признаки: ВЗАИМНЫЙ — обе стороны назвали друг друга синонимом,"+
		" два независимых суждения, и для таких пар порог близости ниже;")
	fmt.Fprintln(stdout, "            синоним — модель связала имена в одну сторону;"+
		" ~синоним — связала, но написание для поиска негодное;")
	fmt.Fprintln(stdout, "            тип≠ — разные типы сущностей; ЦИФРЫ — имена различаются"+
		" только цифрами, это разные версии;")
	fmt.Fprintln(stdout, "            язык — пара через границу алфавита. Стрелка указывает,"+
		" кого предлагать главным.")
	fmt.Fprintln(stdout, "  общих: сколько соседей по графу у пары общие. Прочерк — соседей нет"+
		" хотя бы у одного, сравнивать нечего.")
	fmt.Fprintln(stdout, "\n  Ничего не изменено: команда только показывает.")
	return nil
}

// resolveMarks — признаки пары одной строкой.
func resolveMarks(p graph.ResolvePair) string {
	var m []string
	switch {
	case p.Mutual:
		// Взаимный синоним важнее того, годится ли написание для поиска:
		// это два независимых суждения модели вместо одного.
		m = append(m, "ВЗАИМНЫЙ")
	case p.AliasLink && p.AliasUsable:
		m = append(m, "синоним")
	case p.AliasLink:
		m = append(m, "~синоним")
	}
	if !p.SameType {
		m = append(m, "тип≠")
	}
	if p.DigitDiff {
		m = append(m, "ЦИФРЫ")
	}
	if p.Cross {
		m = append(m, "язык")
	}
	return strings.Join(m, " ")
}

// sharedText — общие соседи. Прочерк означает «сравнивать нечего», а не «ноль»:
// у 67% межалфавитных двойников соседей нет вовсе, и путать это с расхождением
// нельзя.
func sharedText(p graph.ResolvePair) string {
	if p.Jaccard < 0 {
		return "—"
	}
	return strconv.Itoa(p.SharedNb)
}

// trimName укорачивает имя, не разрезая символ пополам.
func trimName(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// writeResolveTSV выписывает все пары для разбора глазами и сторонними
// средствами: список длинный, и листать его в терминале смысла нет.
func writeResolveTSV(g *graph.Graph, pairs []graph.ResolvePair, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := fmt.Fprintln(f, strings.Join([]string{"cos", "синоним", "синоним_годен",
		"взаимный", "тип_совпал", "цифры", "язык", "общих", "жаккар", "id_a", "имя_a", "тип_a",
		"книг_a", "id_b", "имя_b", "тип_b", "книг_b", "главный"}, "\t")); err != nil {
		return err
	}
	for _, p := range pairs {
		a, _ := g.Entities().Get(p.A)
		b, _ := g.Entities().Get(p.B)
		jac := "—"
		if p.Jaccard >= 0 {
			jac = fmt.Sprintf("%.3f", p.Jaccard)
		}
		if _, err := fmt.Fprintf(f, "%.4f\t%t\t%t\t%t\t%t\t%t\t%t\t%d\t%s\t%d\t%s\t%s\t%d\t%d\t%s\t%s\t%d\t%d\n",
			p.Cos, p.AliasLink, p.AliasUsable, p.Mutual, p.SameType, p.DigitDiff, p.Cross,
			p.SharedNb, jac,
			a.ID, a.Name, a.Type, p.BooksA, b.ID, b.Name, b.Type, p.BooksB, p.Keep); err != nil {
			return err
		}
	}
	return nil
}

// Merge применяет решения о склейке двойников из файла TSV.
//
// Файл — тот же `verdicts.tsv`, что даёт разбор моделью: вердикт, признаки
// и обе половины пары. Отбирается по уровню строгости, и только он решает,
// что склеится.
//
// **Склейка снимается целиком** ключом `--graph-merge-drop`: решения лежат
// отдельным журналом и надеваются на граф при чтении. Это единственная защита
// от неверного решения, потому что по смыслу склейка необратима.
func Merge(stdout io.Writer, cfg *config.Config, name, file, level string, minCosSame float64,
	drop, dry bool) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	unmark, err := markWork(g, "склейка двойников")
	if err != nil {
		return err
	}
	defer unmark()

	if drop {
		had := g.Merges().Count()
		if err := g.DropMerges(); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "склейки сняты: было поглощено %d понятий, граф вернулся в прежний вид\n", had)
		return nil
	}

	if file == "" {
		// Без файла команда показывает, что уже склеено.
		recs := g.Merges().Records()
		fmt.Fprintf(stdout, "коллекция %s: поглощено понятий %d, решений в журнале %d\n",
			name, g.Merges().Count(), len(recs))
		for i, r := range recs {
			if i >= 20 {
				fmt.Fprintf(stdout, "  …и ещё %d\n", len(recs)-20)
				break
			}
			a, _ := g.Entities().Get(r.To)
			fmt.Fprintf(stdout, "  %d → %d  cos %.3f  %s  (%s)\n", r.From, r.To, r.Cos, a.Name, r.Why)
		}
		return nil
	}

	pairs, err := readVerdicts(file, level, minCosSame)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "коллекция %s, уровень «%s»", name, level)
	if minCosSame > 0 {
		fmt.Fprintf(stdout, ", для пар внутри одного языка порог %.2f", minCosSame)
	}
	fmt.Fprintf(stdout, ": пар к склейке %d\n", len(pairs))
	if len(pairs) == 0 {
		return nil
	}

	// Кого оставить главным: понятие из большего числа книг. Считается по
	// журналу упоминаний, а не по полю Docs — оно не заполняется вовсе.
	recs := make([]graph.MergeRec, 0, len(pairs))
	for _, p := range pairs {
		keep, gone := p.keep, p.drop
		recs = append(recs, graph.MergeRec{
			From: gone, To: keep, Cos: p.cos, Verdict: p.verdict,
			Alias: p.alias, Why: p.why, Level: level,
		})
	}
	if dry {
		fmt.Fprintln(stdout, "сухой прогон: ничего не записано. Примеры:")
		for i, r := range recs {
			if i >= 15 {
				break
			}
			a, _ := g.Entities().Get(r.To)
			b, _ := g.Entities().Get(r.From)
			fmt.Fprintf(stdout, "  cos %.3f  %s ← %s  (%s)\n", r.Cos, a.Name, b.Name, r.Why)
		}
		return nil
	}

	before := g.Entities().Live()
	n, err := g.Merges().Add(recs)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "записано решений: %d\n", n)
	fmt.Fprintf(stdout, "понятий было %d, стало %d\n", len(before), len(before)-n)
	fmt.Fprintln(stdout, "снять всё: ollchat --graph-merge "+name+" --graph-merge-drop")
	return nil
}

// verdictPair — пара из файла разбора, уже с выбранным главным.
type verdictPair struct {
	keep, drop uint32
	cos        float64
	verdict    string
	why        string
	alias      bool
}

// mergeLevels — правила отбора. Строгость набирается независимыми голосами:
// вердикт разбиравшей модели, синоним от модели извлечения, близость векторов.
// Пары с признаком «цифры» не склеиваются ни на каком уровне — это разные
// версии одного и того же (`load_in_4bit` и `load_in_8bit`).
//
// Уровень `mutual` добавлен 02.09.2026 и опирается на замер по всему графу:
// случайная пара понятий не даёт cos ≥0.80 ни разу из пятидесяти тысяч,
// а взаимный синоним — это два независимых суждения модели извлечения,
// сделанных на разных кусках. Порог 0.95 уровня `strict` отсекал
// `горутина ↔ goroutine` (0.853) — то есть как раз то, ради чего разрешение
// сущностей и делалось.
//
// **Порог опущен с 0.80 до 0.70 в тот же день, и это тоже замер, а не догадка.**
// В полосе 0.70–0.80 оказалось 1311 взаимных пар; `qwen3.8` разобрала их
// с солью 170 из 170 и ловушками 0 из 7, то есть арбитру там можно верить.
// Настоящими двойниками из них оказалась треть — 403 пары: `ELF ↔ ELF format`,
// `Linux ↔ Linux OS`, `compliance ↔ комплаенс`, `net/http ↔ пакет net/http`
// и обе формы множественного числа `goroutines ↔ горутины` (0.703), из-за
// которых вопрос во множественном числе попадал в узел на 129 упоминаний
// вместо 3726. Ниже 0.70 не идём: там доля верных пар падает, а случайные
// пары начинают попадаться (0.03% против нуля выше 0.80).
var mergeLevels = map[string]func(v verdictFacts) bool{
	"strict":  func(v verdictFacts) bool { return v.alias && v.cos >= 0.95 },
	"alias":   func(v verdictFacts) bool { return v.alias },
	"vector":  func(v verdictFacts) bool { return v.cos >= 0.95 },
	"mixed":   func(v verdictFacts) bool { return v.alias || v.cos >= 0.95 },
	"mutual":  func(v verdictFacts) bool { return v.mutual && v.cos >= 0.70 },
	"soft":    func(v verdictFacts) bool { return v.cos >= 0.92 },
	"all-yes": func(v verdictFacts) bool { return true },
}

// verdictFacts — признаки пары, по которым уровень решает, склеивать ли.
// Структура, а не список доводов: список уже дорос до трёх, и следующий
// признак снова переписывал бы все шесть строк выше.
type verdictFacts struct {
	cos    float64
	alias  bool
	mutual bool
}

// MergeLevelNames — уровни строгости для справки.
func mergeLevelNames() string {
	return "strict (ДА+синоним+cos≥0.95), alias (ДА+синоним), vector (ДА+cos≥0.95), " +
		"mixed (ДА+любой из двух), mutual (ДА+взаимный синоним+cos≥0.80), " +
		"soft (ДА+cos≥0.92), all-yes (любое ДА)"
}

// readVerdicts читает разбор и отбирает пары по уровню строгости.
//
// minCosSame — отдельный порог близости для пар **внутри одного языка**.
// Замер 27.08.2026: перевод и пересказ различаются на шесть тысячных, то есть
// смена языка модели почти ничего не стоит, а смена предмета уводит вдвое
// дальше. Поэтому «граф знаний» и `knowledge graph` при близости 0.90 —
// почти наверняка одно и то же, а `Agentic systems` и `Agentic AI Systems`
// при той же близости — скорее широкое и узкое. Ложных друзей переводчика
// в технической лексике единицы, а вот обобщение с частностью внутри языка
// встречается постоянно.
func readVerdicts(path, level string, minCosSame float64) ([]verdictPair, error) {
	pick, ok := mergeLevels[level]
	if !ok {
		return nil, fmt.Errorf("неизвестный уровень %q; есть: %s", level, mergeLevelNames())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !sc.Scan() {
		return nil, fmt.Errorf("%s пуст", path)
	}
	col := map[string]int{}
	for i, c := range strings.Split(sc.Text(), "\t") {
		col[strings.TrimSpace(c)] = i
	}
	for _, need := range []string{"вердикт", "cos", "синоним", "цифры", "язык", "id_a", "id_b", "книг_a", "книг_b"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("в %s нет столбца %q", path, need)
		}
	}

	var out []verdictPair
	for sc.Scan() {
		row := strings.Split(sc.Text(), "\t")
		if len(row) < len(col) {
			continue
		}
		get := func(k string) string { return strings.TrimSpace(row[col[k]]) }
		if get("вердикт") != "ДА" || get("цифры") == "true" {
			continue
		}
		cos, _ := strconv.ParseFloat(get("cos"), 64)
		alias := get("синоним") == "true"
		// Столбца «взаимный» в разборах, сделанных до 02.09.2026, нет:
		// отсутствие читается как «не взаимный», и прежние файлы разбираются
		// прежними уровнями без изменений.
		mutual := colOr(row, col, "взаимный") == "true"
		if !pick(verdictFacts{cos: cos, alias: alias, mutual: mutual}) {
			continue
		}
		if minCosSame > 0 && get("язык") != "true" && cos < minCosSame {
			continue
		}
		a64, err1 := strconv.ParseUint(get("id_a"), 10, 32)
		b64, err2 := strconv.ParseUint(get("id_b"), 10, 32)
		if err1 != nil || err2 != nil {
			continue
		}
		ka, _ := strconv.Atoi(get("книг_a"))
		kb2, _ := strconv.Atoi(get("книг_b"))
		keep, gone := uint32(a64), uint32(b64)
		// Главным остаётся понятие из большего числа книг: у него надёжнее
		// связи. При равенстве — заведённое раньше, ради повторяемости.
		if kb2 > ka || (kb2 == ka && b64 < a64) {
			keep, gone = gone, keep
		}
		out = append(out, verdictPair{keep: keep, drop: gone, cos: cos,
			verdict: "ДА", alias: alias, why: colOr(row, col, "причина")})
	}
	return out, sc.Err()
}

func colOr(row []string, col map[string]int, name string) string {
	if i, ok := col[name]; ok && i < len(row) {
		return strings.TrimSpace(row[i])
	}
	return ""
}

// share — доля в процентах, без деления на ноль.
func share(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}

// Compact уплотняет реестр понятий: по одной записи на понятие вместо
// двух десятков. Открытие графа после этого идёт секунды вместо десятков секунд.
//
// Команда осторожна нарочно: реестр — это недели работы видеокарты. Прежний
// файл остаётся рядом, а подмена происходит только если словари поиска обоих
// реестров совпали до последнего ключа.
func Compact(stdout io.Writer, cfg *config.Config, name string, check, force bool) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}

	if !check {
		unmark, err := graph.MarkWork(cfg.Graph.Rules().Dir(coll.Dir()), "уплотнение реестра")
		if err != nil {
			return err
		}
		defer unmark()
	}
	fmt.Fprintf(stdout, "коллекция %s: читаю реестр понятий, сличаю словари — это минута-две…\n", name)
	st, err := graph.Compact(coll.Dir(), cfg.Graph.Name, check, force)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "записей в реестре %d → %d, размер %s → %s\n",
		st.RecordsBefore, st.RecordsAfter,
		fsx.HumanSize(st.BytesBefore), fsx.HumanSize(st.BytesAfter))

	fmt.Fprintf(stdout, "словарь имён: ключей %d, разошлось %d (%.2f%%); основ слов %d, разошлось %d (%.2f%%)\n",
		st.Keys, st.KeysDiff, share(st.KeysDiff, st.Keys),
		st.Stems, st.StemsDiff, share(st.StemsDiff, st.Stems))

	if len(st.Diffs) == 0 {
		fmt.Fprintln(stdout, "словари поиска совпали полностью: ключи и основы слов ведут к тем же понятиям")
	} else {
		fmt.Fprintf(stdout, "РАСХОЖДЕНИЯ СЛОВАРЕЙ (%d показано):\n", len(st.Diffs))
		for _, d := range st.Diffs {
			fmt.Fprintln(stdout, "  "+d)
		}
	}

	switch {
	case st.Applied:
		fmt.Fprintf(stdout, "реестр уплотнён. Прежний файл: %s\n", st.Backup)
		fmt.Fprintln(stdout, "проверьте поиск и удалите его сами — программа чужого не трёт")
	case check:
		fmt.Fprintln(stdout, "это была проверка: реестр не тронут")
	case len(st.Diffs) > 0:
		fmt.Fprintln(stdout, "реестр НЕ тронут: сначала разберитесь с расхождениями выше.")
		fmt.Fprintln(stdout, "если они приемлемы — повторите с --graph-compact-force")
	}
	return nil
}

// Book показывает вклад одной книги в граф: сколько понятий и связей
// она дала и что из этого держится только на ней. Ничего не меняет.
//
// Это выборка по номеру книги (Doc записан у каждого упоминания и связи),
// а не новое хранение. Основа для будущего «выбросить вклад книги и переизвлечь
// её» без пересборки всего графа — см. GraphSchemaV2.md и todo-clean-ai-books.
func Book(stdout io.Writer, cfg *config.Config, name, bookQuery string) error {
	if bookQuery == "" {
		return fmt.Errorf("нужна часть имени книги: --graph-book-name <строка>")
	}
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	books := coll.MatchingDocs(kb.ChunkFilter{PathContains: bookQuery})
	if len(books) == 0 {
		return fmt.Errorf("книга по «%s» не найдена в коллекции %s", bookQuery, name)
	}

	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	printOpen(stdout, g, cfg)

	for _, b := range books {
		c := g.Contribution(b.ID)
		title := b.Title
		if title == "" {
			title = b.Path
		}
		fmt.Fprintf(stdout, "\n%s\n", title)
		fmt.Fprintf(stdout, "  упоминаний понятий: %d, связей: %d, понятий: %d\n",
			c.Mentions, c.Edges, len(c.Entities))
		fmt.Fprintf(stdout, "  держится только на этой книге: %d понятий\n", len(c.OnlyHere))
		// Показать несколько таких понятий: по ним видна цена отбрасывания книги.
		shown := 0
		for _, id := range c.OnlyHere {
			ent, ok := g.Entities().Get(id)
			if !ok {
				continue
			}
			fmt.Fprintf(stdout, "    · %s (%s)\n", ent.Name, ent.Type)
			if shown++; shown >= 15 {
				fmt.Fprintf(stdout, "    …и ещё %d\n", len(c.OnlyHere)-shown)
				break
			}
		}
	}
	return nil
}

// DropBook скрывает вклад книги из графа (или возвращает его).
//
// Это представление, а не удаление: вклад книги перестаёт показываться в выдаче,
// но реестр понятий и журналы целы, а решение лежит отдельной строкой в
// dropped-books.jsonl и снимается обратно. Задумано под чистку испорченной
// книги без пересборки всего графа — см. GraphSchemaV2.md, todo-clean-ai-books.
func DropBook(stdout io.Writer, cfg *config.Config, name, bookQuery string, restore, apply bool) error {
	if bookQuery == "" {
		return fmt.Errorf("нужна часть имени книги: --graph-book-name <строка>")
	}
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	books := coll.MatchingDocs(kb.ChunkFilter{PathContains: bookQuery})
	if len(books) == 0 {
		return fmt.Errorf("книга по «%s» не найдена в коллекции %s", bookQuery, name)
	}

	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	unmark, err := markWork(g, "скрытие книги")
	if err != nil {
		return err
	}
	defer unmark()
	printOpen(stdout, g, cfg)

	action := "скрыть вклад"
	if restore {
		action = "вернуть"
	}
	for _, b := range books {
		title := b.Title
		if title == "" {
			title = b.Path
		}
		already := g.Dropped().Dropped(b.ID)
		c := g.Contribution(b.ID)

		if restore {
			if !already {
				fmt.Fprintf(stdout, "книга не была скрыта, возвращать нечего: %s\n", title)
				continue
			}
		} else if already {
			fmt.Fprintf(stdout, "книга уже скрыта: %s\n", title)
			continue
		}

		fmt.Fprintf(stdout, "\n%s: %s\n", action, title)
		fmt.Fprintf(stdout, "  затронуто: %d упоминаний, %d связей, %d понятий\n",
			c.Mentions, c.Edges, len(c.Entities))
		if !restore {
			fmt.Fprintf(stdout, "  из них исчезнут из выдачи целиком (держатся только на этой книге): %d понятий\n",
				len(c.OnlyHere))
		}

		if !apply {
			fmt.Fprintln(stdout, "  СУХОЙ ПРОГОН: ничего не изменено. Повторите с --apply.")
			continue
		}
		if restore {
			err = g.RestoreBook(b.ID, b.Path)
		} else {
			err = g.DropBook(b.ID, b.Path, "ручное отбрасывание")
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "  применено. Откат — тем же действием с обратным ключом.")
	}
	if apply {
		fmt.Fprintf(stdout, "\nскрыто книг в графе: %d\n", g.Dropped().Count())
	}
	return nil
}

// GroupsBuild наполняет журнал групп понятий.
//
// Источник задаётся --from: merges (перевести накопленные склейки в мягкие
// группы), resolve (вычислить пары «похоже» и собрать их в связные компоненты),
// both (склейки, если они есть, иначе вычислить). Решение владельца 03.09.2026:
// источник — ключ команды, а не настройка конфига: команда порождает данные,
// и делать это молча по строке в конфиге нельзя.
func GroupsBuild(stdout io.Writer, cfg *config.Config, name, from string, minCos, minCosMutual float64) error {
	switch from {
	case "merges", "resolve", "both":
	default:
		return fmt.Errorf("--from: ожидается merges, resolve или both, получено %q", from)
	}

	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()
	coll, err := base.Open(name)
	if err != nil {
		return err
	}
	g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules())
	if err != nil {
		return err
	}
	defer g.Close()
	unmark, err := markWork(g, "группы понятий")
	if err != nil {
		return err
	}
	defer unmark()
	printOpen(stdout, g, cfg)

	// both: склейки, если они есть; иначе вычисление.
	src := from
	if src == "both" {
		if len(g.PairsFromMerges()) > 0 {
			src = "merges"
		} else {
			src = "resolve"
		}
		fmt.Fprintf(stdout, "источник both → %s\n", src)
	}

	var pairs [][2]uint32
	conf := 1.0
	why := ""
	switch src {
	case "merges":
		pairs = g.PairsFromMerges()
		why = "из склеек двойников"
		if len(pairs) == 0 {
			return fmt.Errorf("склеек нет: нечего переводить в группы (попробуйте --from resolve)")
		}
	case "resolve":
		fmt.Fprintln(stdout, "вычисляю пары похожих понятий по векторам и синонимам…")
		rp, _, err := g.ResolveCandidates(graph.ResolveOpts{MinCos: minCos, MinCosMutual: minCosMutual})
		if err != nil {
			return err
		}
		for _, p := range rp {
			pairs = append(pairs, [2]uint32{p.A, p.B})
		}
		why = "вычислено по близости и взаимным синонимам"
		conf = 0.8 // вычисленная группа менее уверенна, чем прошедшая арбитра склейка
		if len(pairs) == 0 {
			return fmt.Errorf("похожих пар не найдено при этих порогах")
		}
	}

	groups, members, err := g.GroupsFromPairs(pairs, conf, why, src)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "собрано групп: %d, охвачено понятий: %d (источник %s)\n", groups, members, src)
	fmt.Fprintf(stdout, "всего групп в графе: %d\n", g.Groups().Count())
	fmt.Fprintln(stdout, "откат: удалить groups.jsonl или снять группы по одной (Undo)")
	return nil
}

// markWork ставит признак пишущей работы в каталоге графа — по нему архив
// коллекции откладывается, пока работа идёт (см. internal/graph/work.go).
func markWork(g *graph.Graph, what string) (func(), error) {
	unmark, err := graph.MarkWork(g.Dir(), what)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return unmark, nil
}
