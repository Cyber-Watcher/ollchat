package maint

import (
	"bufio"
	"context"
	"fmt"
	"github.com/Cyber-Watcher/ollchat/internal/textx"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbembed"

	"golang.org/x/term"
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

// Index собирает или обновляет коллекцию без запуска интерфейса.
// kbIndexDryRun показывает, что сделала бы индексация, ничего не записывая.
//
// Для --kb-sync считает Pending: сколько книг появилось и сколько пропало.
// Для --kb-index — сколько книг нашлось бы в указанных каталогах.
func kbIndexDryRun(stdout io.Writer, coll *kb.Collection, name string, paths []string, sync bool) error {
	st := coll.Stats()
	if sync {
		p, err := coll.Pending()
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "сухой прогон: коллекция %s\n", name)
		fmt.Fprintf(stdout, "  добавилось бы книг: %d\n", len(p.Files))
		fmt.Fprintf(stdout, "  перечиталось бы изменившихся: %d\n", len(p.Changed))
		fmt.Fprintf(stdout, "  пропавших с диска:  %d\n", p.Missing)
		fmt.Fprintf(stdout, "сейчас в коллекции: книг %d, кусков %d\n", st.Indexed, st.Chunks)
		if p.New == 0 && p.Missing == 0 {
			fmt.Fprintln(stdout, "делать нечего — коллекция совпадает с папками")
			return nil
		}
		fmt.Fprintf(stdout, "выполнить: ollchat --kb-sync %s\n", name)
		return nil
	}

	fmt.Fprintf(stdout, "сухой прогон: коллекция %s, каталоги %v\n", name, paths)
	fmt.Fprintf(stdout, "сейчас в коллекции: книг %d, кусков %d\n", st.Indexed, st.Chunks)
	fmt.Fprintf(stdout, "выполнить: ollchat --kb-index %s %s\n", name, strings.Join(paths, " "))
	return nil
}

// Index собирает или досверяет коллекцию.
//
// dry — только показать, что будет сделано. Ключ --kb-dry-run раньше действовал
// лишь на --kb-embed, и «сухой» --kb-sync 29.08.2026 доиндексировал 49 книг
// по-настоящему: человек просил оценку, а получил работу. Оценка обязана быть
// оценкой при любой команде, к которой её приписали.
func Index(stdout io.Writer, cfg *config.Config, name string, paths []string, sync, dry bool) error {
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
		allowed, err := AllowedPaths(cfg.KB.Roots, paths)
		if err != nil {
			return err
		}
		if !dry {
			// Каталоги коллекции — тоже запись: при сухом прогоне не трогаем.
			if err := coll.AddRoots(allowed); err != nil {
				return err
			}
		}
		paths = allowed
	}

	if dry {
		return kbIndexDryRun(stdout, coll, name, paths, sync)
	}

	// Ctrl+C прерывает работу, а не убивает её: индексация успевает
	// зафиксировать текущую книгу.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	pl := progressPrinter()
	defer pl.stop()
	report := pl.update
	opt := kb.IndexOpts{Workers: cfg.KB.Workers, MaxBytes: int64(cfg.KB.MaxBookMB) << 20}

	var res kb.IndexResult
	if sync {
		res, err = coll.Sync(ctx, report)
	} else {
		res, err = coll.Add(ctx, paths, opt, report)
	}
	pl.stop()
	if err != nil {
		return err
	}

	st := coll.Stats()
	fmt.Fprintf(stdout, "коллекция %s: добавлено книг %d", name, res.Added)
	if res.Removed > 0 {
		fmt.Fprintf(stdout, ", убрано %d", res.Removed)
	}
	if res.Scans > 0 {
		fmt.Fprintf(stdout, ", сканов без текста %d", res.Scans)
	}
	// **Пропущенные обязаны быть в итоге.** Их не печатали вовсе, и на трёх
	// книгах в чужой кодировке итог выглядел как «добавлено книг 0» — без
	// единого слова о том, что три книги нашлись, но не прочитались.
	// Поймано 30.08.2026 на своём же проверочном наборе.
	if res.Skipped > 0 {
		fmt.Fprintf(stdout, ", пропущено %d", res.Skipped)
	}
	if res.Errors > 0 {
		fmt.Fprintf(stdout, ", сбоев %d", res.Errors)
	}
	fmt.Fprintf(stdout, "\n")
	if res.Skipped+res.Scans+res.Errors > 0 {
		fmt.Fprintf(stdout, "причины по каждой книге: ollchat --kb-doctor %s\n", name)
	}
	printAdded(stdout, res.AddedBooks)
	if rep := kb.DuplicateReport(res.Duplicates); rep != "" {
		fmt.Fprint(stdout, rep)
	}
	fmt.Fprintf(stdout, "всего: книг %d, кусков %d, термов %d, на диске %.1f МБ, за %s\n",
		st.Indexed, st.Chunks, st.Terms, float64(st.Bytes)/1e6, time.Since(start).Round(time.Second))
	if res.Canceled {
		fmt.Fprintln(stdout, "работа прервана; продолжить: та же команда — она перечитает только недостающее")
	}
	return nil
}

// Merge уплотняет коллекцию без запуска интерфейса.
//
// Держать окно открытым ради переписывания четверти миллиона кусков незачем,
// а Ctrl+C безопасен: коллекция остаётся прежней целиком.
func Merge(stdout io.Writer, cfg *config.Config, name string, force, yes, dry bool) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}

	// Сначала — что именно произойдёт, в числах. Потом два подтверждения.
	pv := mergePreview(coll)
	fmt.Fprint(stdout, pv.explain(name))
	if dry {
		fmt.Fprintln(stdout, "\n--kb-dry-run: ничего не сделано.")
		return nil
	}
	if err := confirmMerge(name, pv, force, yes); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pl := progressPrinter()
	defer pl.stop()
	res, err := coll.Merge(ctx, kb.MergeOpts{Force: force}, pl.update)
	pl.stop()
	if err != nil {
		return err
	}
	if res.Canceled {
		fmt.Fprintln(stdout, "уплотнение прервано; коллекция осталась прежней")
		return nil
	}
	fmt.Fprintf(stdout, "коллекция %s уплотнена за %s\n", name, res.Elapsed.Round(time.Second))
	fmt.Fprintf(stdout, "  книг выброшено: %d\n", res.BooksDropped)
	fmt.Fprintf(stdout, "  кусков: %d → %d\n", res.ChunksBefore, res.ChunksAfter)
	fmt.Fprintf(stdout, "  сегментов: %d → %d\n", res.SegmentsBefore, res.SegmentsAfter)
	fmt.Fprintf(stdout, "  на диске: %.1f → %.1f МБ\n", float64(res.BytesBefore)/1e6, float64(res.BytesAfter)/1e6)
	return nil
}

// Embed считает векторы(смыслы) коллекции без запуска интерфейса.
//
// Работа долгая, поэтому ей самое место под nohup. Ctrl+C безопасен:
// посчитанное сохранено, следующий запуск продолжит с того же места.
// Refresh доливает новое и тут же досчитывает смыслы к нему.
//
// **Зачем отдельной командой.** Куски и векторы устаревают по-разному, и это
// молчаливое расхождение: `--kb-sync` добавляет куски, а смысловой поиск их
// не видит, пока не посчитаны векторы, — и **не сообщает об этом**, он просто
// их не находит, а выдача выглядит нормальной. 28.08.2026 так набежало 15%
// кусков коллекции документации: индекс доливался хуком после каждой правки,
// а векторы никто не считал.
//
// Помнить про две команды вместо одной — плохая опора. Здесь они связаны.
//
// Смыслы досчитываются только тем, у кого их нет: покрытие всегда начальный
// отрезок, потому что куски дописываются в конец. Поэтому доливка десятка
// файлов стоит секунды, а не пересчёта всей коллекции.
func Refresh(stdout io.Writer, cfg *config.Config, name string, dry bool) error {
	if err := Index(stdout, cfg, name, nil, true, dry); err != nil {
		return err
	}
	// Смыслы не настроены — это не ошибка доливки: словесный поиск работает
	// и без них, и человек мог их сознательно не включать.
	if cfg.KB.EmbedModel == "" || !cfg.KB.Semantic {
		return nil
	}
	fmt.Fprintln(stdout)
	return Embed(stdout, cfg, name, dry)
}

// EstimateTimeout — сколько ждать замер скорости при сухом прогоне.
//
// Оценка не стоит долгого ожидания: человек спрашивает «сколько это займёт»
// и вправе получить ответ быстрее, чем за минуту.
const EstimateTimeout = 60 * time.Second

func Embed(stdout io.Writer, cfg *config.Config, name string, dry bool) error {
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
	emb := kbembed.New(cfg.KB.EmbedOptions(), fallback, timeout, nil)
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
		// Оценка делается замером на 200 кусках — это честнее любой формулы.
		// Но сервер бывает занят: пока идёт сборка графа, он не отдаёт
		// эмбеддинги вовсе (замер 29.08.2026), и «оценка» вставала на четверть
		// часа. Поэтому у замера свой короткий срок, а без него — счёт по тому,
		// что известно локально.
		ectx, ecancel := context.WithTimeout(ctx, EstimateTimeout)
		res, err := coll.EstimateEmbed(ectx, emb, opt, 200)
		ecancel()
		if err != nil {
			st := coll.Stats()
			fmt.Fprintf(stdout, "коллекция %s, модель %s (%s)\n", name, emb.Model(), emb.URL())
			fmt.Fprintf(stdout, "  осталось кусков: %d из %d\n", st.Chunks-st.Vectors, st.Chunks)
			fmt.Fprintln(stdout, "  время не замерено: сервер эмбеддингов не ответил за "+
				EstimateTimeout.String()+" ("+err.Error()+")")
			fmt.Fprintln(stdout, "  так бывает, пока карту занимает сборка графа — она не отдаёт эмбеддинги вовсе")
			return nil
		}
		if res.Added == 0 {
			fmt.Fprintln(stdout, "векторы(смыслы) уже посчитаны для всей коллекции")
			return nil
		}
		fmt.Fprintf(stdout, "коллекция %s, модель %s (%s)\n", name, emb.Model(), emb.URL())
		fmt.Fprintf(stdout, "  осталось кусков: %d из %d\n", res.Added, res.Total)
		fmt.Fprintf(stdout, "  размерность: %d\n", res.Dim)
		fmt.Fprintf(stdout, "  ориентировочно: %s\n", res.Elapsed.Round(time.Second))
		fmt.Fprintf(stdout, "  на диске ещё: %.0f МБ\n", float64(res.Bytes)/1e6)
		fmt.Fprintln(stdout, "оценка сделана замером на 200 кусках; посчитать: ollchat --kb-embed "+name)
		return nil
	}

	start := time.Now()
	pl := progressPrinter()
	defer pl.stop()
	res, err := coll.Embed(ctx, emb, opt, pl.update)
	pl.stop()
	if err != nil {
		return err
	}
	if res.Canceled {
		fmt.Fprintf(stdout, "прервано: посчитано %d из %d кусков; продолжить — та же команда\n", res.Covered, res.Total)
		return nil
	}
	fmt.Fprintf(stdout, "коллекция %s: векторы(смыслы) посчитаны за %s\n", name, time.Since(start).Round(time.Second))
	fmt.Fprintf(stdout, "  кусков: %d из %d, размерность %d, модель %s\n", res.Covered, res.Total, res.Dim, res.Model)
	fmt.Fprintf(stdout, "  на диске: %.1f МБ\n", float64(res.Bytes)/1e6)
	return nil
}

// List печатает состояние базы знаний.
func List(stdout io.Writer, cfg *config.Config) error {
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
		fmt.Fprintf(stdout, "база знаний пуста: %s\nсобрать: ollchat --kb-index go /путь/к/книгам\n", cfg.KB.Dir)
		return nil
	}
	fmt.Fprintf(stdout, "%-16s %7s %9s %8s %9s %8s\n",
		"коллекция", "книг", "кусков", "сегм.", "размер", "векторы")
	for _, n := range names {
		c, err := base.Open(n)
		if err != nil {
			fmt.Fprintf(stdout, "%-16s  — %s\n", n, err)
			continue
		}
		st := c.Stats()
		// Смыслы устаревают молча: поиск не находит того, чего нет в векторах,
		// и не жалуется. Показываем покрытие, чтобы расхождение было видно
		// сразу, а не после жалобы «плохо ищется».
		sense := "—"
		switch {
		case st.Vectors == 0:
			sense = "нет"
		case st.Vectors >= st.Chunks:
			sense = "все"
		default:
			sense = fmt.Sprintf("%d%%", 100*st.Vectors/max(st.Chunks, 1))
		}
		fmt.Fprintf(stdout, "%-16s %7d %9d %8d %8.1fМБ %8s\n",
			n, st.Indexed, st.Chunks, st.Segments, float64(st.Bytes)/1e6, sense)
	}
	return nil
}

// progressPrinter печатает ход работы одной обновляемой строкой.
// progressPrinter показывает ход работы: сделано, скорость, сколько осталось.
//
// **Почему по таймеру, а не по событию.** Сообщения о ходе приходят пачками:
// при счёте векторов одна пачка — полторы тысячи кусков, то есть строка стояла
// бы неподвижно по двадцать пять секунд. Со стороны это выглядит как
// зависание, и человек идёт выяснять, что сломалось, — хотя всё идёт.
// Поэтому строка перерисовывается раз в секунду, даже когда новых сообщений
// не было: видно, что время идёт и оценка меняется.
//
// **Скорость считается по окну, а не от начала.** Средняя от старта врёт после
// любой заминки: сервер эмбеддингов задумался на минуту — и оценка остатка
// ещё полчаса тянет за собой эту минуту. Окно в тридцать секунд показывает,
// как идёт дело **сейчас**.
// **Возвращается сама строка, а не только её update.** Строка живёт по таймеру
// и гаснет по сообщению Done. Значит, забытый Done где-нибудь в глубине
// оставляет тикающую горутину, которая до конца работы программы раз в секунду
// пишет поверх всего, что печатается следом. Ровно это и случилось
// (замер 30.08.2026, `--kb-refresh books`): ранний выход `Add` не слал Done,
// брошенная строка писала «обход», следом счёт смыслов завёл вторую строку —
// и обе перерисовывали одно место экрана, затирая заодно и список добавленных
// книг. Причину я починил там, где она была, но полагаться на дисциплину
// «не забыть Done» в семи местах нельзя: `defer p.stop()` у вызывающего
// закрывает этот класс ошибок целиком.
func progressPrinter() *progressLine {
	p := &progressLine{start: time.Now(), tty: isTTY(os.Stderr)}
	p.tick()
	return p
}

// progressLine — состояние строки хода работы.
type progressLine struct {
	mu    sync.Mutex
	start time.Time

	phase   string
	current string
	done    int
	total   int
	chunks  int64

	// Окно замера скорости: отметка и число сделанного на ней.
	markAt   time.Time
	markDone int
	rate     float64 // единиц в секунду, 0 — ещё не измерено

	stopped bool
	timer   *time.Ticker
	tty     bool // в терминале строку можно стереть, в журнале — только закрыть

	listed bool // список к разбору печатается один раз
}

func (p *progressLine) update(pr kb.Progress) {
	p.mu.Lock()
	if pr.Done {
		// **Дорисовать до конца, а не бросить где застало.** Последнее сообщение
		// о ходе приходит раньше конца работы, и строка оставалась на экране
		// с «75%» у законченного дела — читается как оборванная работа.
		if p.total > 0 {
			p.done = p.total
		}
		p.stopped = false // разрешаем последнюю отрисовку
		p.mu.Unlock()
		p.draw()
		p.mu.Lock()
		p.stop()
		p.mu.Unlock()
		return
	}
	if len(pr.Files) > 0 && !p.listed {
		p.listed = true
		p.mu.Unlock()
		p.printQueue(pr.Files)
		p.mu.Lock()
	}
	p.phase, p.current = pr.Phase, pr.Current
	p.done, p.total, p.chunks = pr.DocsDone, pr.DocsTotal, pr.Chunks

	// Скорость по окну: отметка сдвигается не чаще раза в тридцать секунд,
	// иначе на редких сообщениях окно вырождается в «между двумя пачками».
	now := time.Now()
	if p.markAt.IsZero() {
		p.markAt, p.markDone = now, pr.DocsDone
	} else if d := now.Sub(p.markAt); d >= 30*time.Second {
		if n := pr.DocsDone - p.markDone; n > 0 {
			p.rate = float64(n) / d.Seconds()
		}
		p.markAt, p.markDone = now, pr.DocsDone
	} else if p.rate == 0 && d >= 3*time.Second {
		// Первая оценка нужна раньше, чем через полминуты: без неё человек
		// первые тридцать секунд не видит ни скорости, ни остатка.
		if n := pr.DocsDone - p.markDone; n > 0 {
			p.rate = float64(n) / d.Seconds()
		}
	}
	p.mu.Unlock()
	p.draw()
}

// printQueue печатает, что программа собирается разбирать.
//
// **До работы, а не после.** Список добавленного отвечает на вопрос «что
// получилось», но человек, положивший книги в каталог, спрашивает раньше и
// другое: «нашлись ли они вообще». Доливка по большой библиотеке идёт минутами,
// и всё это время он смотрит на счётчик, не зная, те ли книги за ним стоят.
//
// Список целиком, без «и ещё N»: он для того и печатается, чтобы сверить его
// глазами с тем, что клали в каталог. Обрезанный для этого негоден.
func (p *progressLine) printQueue(files []string) {
	if p.tty {
		fmt.Fprint(os.Stderr, "\r\033[K") // строка хода не должна попасть в список
	}
	fmt.Fprintf(os.Stdout, "к разбору %s:\n", booksWordCount(len(files)))
	for _, f := range files {
		fmt.Fprintf(os.Stdout, "  · %s\n", filepath.Base(f))
	}
	fmt.Fprintln(os.Stdout)
}

// booksWordCount склоняет «N книг»: список читает человек.
func booksWordCount(n int) string {
	word := "книг"
	switch {
	case n%10 == 1 && n%100 != 11:
		word = "книга"
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		word = "книги"
	}
	return fmt.Sprintf("%d %s", n, word)
}

// tick заводит перерисовку раз в секунду.
func (p *progressLine) tick() {
	p.timer = time.NewTicker(time.Second)
	go func() {
		for range p.timer.C {
			p.mu.Lock()
			stopped := p.stopped
			p.mu.Unlock()
			if stopped {
				return
			}
			p.draw()
		}
	}()
}

// stop гасит строку и убирает её за собой.
//
// В терминале — стирает: следом печатается итог, и обрывок строки хода прилип
// бы к его первой строке. В журнале (вывод перенаправлен в файл) стирать нечего
// и незачем — там строка закрывается переводом строки и остаётся следом того,
// как шла работа.
func (p *progressLine) stop() {
	if p.stopped {
		return
	}
	p.stopped = true
	if p.timer != nil {
		p.timer.Stop()
	}
	if p.tty {
		fmt.Fprint(os.Stderr, "\r\033[K")
	} else {
		fmt.Fprintln(os.Stderr)
	}
}

func (p *progressLine) draw() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || p.phase == "" {
		return
	}

	line := p.phase
	if p.total > 0 {
		pct := 100 * p.done / p.total
		line += " " + bar(pct) + fmt.Sprintf(" %3d%% %d/%d", pct, p.done, p.total)
	}
	if p.rate > 0 {
		line += fmt.Sprintf(" · %.0f/с", p.rate)
		if left := p.total - p.done; left > 0 {
			line += " · ещё ~" + humanLeft(time.Duration(float64(left)/p.rate)*time.Second)
		}
	}
	if p.chunks > 0 {
		line += fmt.Sprintf(" · кусков %d", p.chunks)
	}
	if p.current != "" {
		line += " · " + p.current
	}
	if len([]rune(line)) > 110 {
		line = string([]rune(line)[:109]) + "…"
	}
	fmt.Fprintf(os.Stderr, "\r%-110s", line)
}

// bar рисует полосу заполнения.
//
// Знаками рамки, а не «#»: они одной ширины в любом моноширинном шрифте
// и не сливаются с текстом вокруг. Ширина невелика нарочно — строка несёт
// ещё скорость, остаток и имя книги, и полоса не должна их вытеснять.
func bar(pct int) string {
	const width = 20
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	full := pct * width / 100
	return "[" + strings.Repeat("█", full) + strings.Repeat("░", width-full) + "]"
}

// humanLeft печатает остаток так, как его читает человек: «3 мин», «1 ч 20 мин».
//
// Не «1h20m0s»: единицы времени в ответе программы человек читает глазами,
// а не разбирает программой.
func humanLeft(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d с", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d мин", int(d.Minutes()))
	default:
		h := int(d.Hours())
		return fmt.Sprintf("%d ч %d мин", h, int(d.Minutes())-h*60)
	}
}

// printAdded перечисляет добавленные книги.
//
// **Зачем список, если есть счётчик.** Книги кладут в каталог руками, и «добавлено
// книг 7» не отличает семь нужных от семи случайно попавших в папку — от чужого
// сборника, от дубля под другим именем, от книги, вложенной в архив вместе
// с нужными. Проверить это глазами человек может только сразу и только здесь:
// потом придётся сверять состав коллекции с тем, что он помнит.
//
// Год печатается рядом с названием: знание из книги датировано, и подсунутое
// издание двадцатилетней давности видно сразу.
//
// Длинный список сворачивается: при доливке сотен книг перечислять все — значит
// залить экран и спрятать итоговую строку, ради которой команду и запускали.
func printAdded(stdout io.Writer, books []kb.AddedBook) {
	if len(books) == 0 {
		return
	}
	// **Список целиком, без «и ещё N».** Он существует ровно затем, чтобы
	// сверить глазами добавленное с тем, что клали в каталог; обрезанный
	// список для этого негоден — а обрезался он как раз на большой доливке,
	// то есть тогда, когда нужен больше всего.
	for _, b := range books {
		year := ""
		if b.Year > 0 {
			year = fmt.Sprintf(" · %d г.", b.Year)
		}
		fmt.Fprintf(stdout, "  + %s%s · кусков %d\n", b.Title, year, b.Chunks)
	}
}

// AllowedPaths проверяет, что каталоги разрешены настройкой kb.roots.
//
// Тот же список, что и в приложении: книги лежат вне песочницы, и разрешение
// на них даёт только файл настроек. Безголовый режим здесь ничем не вольнее.
func AllowedPaths(roots []string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("не указано ни одного каталога с книгами")
	}
	if len(roots) == 0 {
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
		for _, r := range roots {
			if within(r, abs) {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("каталог %s вне разрешённых kb.roots:\n  %s",
				abs, strings.Join(roots, "\n  "))
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

// Years проставляет книгам коллекции год издания.
//
// Отдельной командой и без интерфейса: у библиотеки в сотни книг проход
// занимает минуты, а не часы — год почти всегда лежит в имени файла,
// и открывать приходится немногие.
func Years(stdout io.Writer, cfg *config.Config, name string, force bool) error {
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

	started := time.Now()
	maxBytes := int64(cfg.KB.MaxBookMB) * 1024 * 1024
	res, err := coll.RefreshYears(ctx, maxBytes, force, func(p kb.YearsProgress) {
		fmt.Fprintf(os.Stderr, "\r\033[K%d/%d книг · с годом %d · без года %d · открыто %d · %s",
			p.Done, p.Total, p.Found, p.Missing, p.Opened, textx.Shorten(p.Book, 40+1))
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "коллекция %s: год проставлен %d книгам из %d за %s\n",
		name, res.Found, res.Total, time.Since(started).Round(time.Second))
	fmt.Fprintf(stdout, "  из имени файла: %d, открыто файлов: %d, без года: %d\n",
		res.Named, res.Opened, res.Missing)
	if res.Missing > 0 {
		fmt.Fprintln(stdout, "  книги без года не хуже прочих: они просто отвечают без оговорки о давности")
	}
	return nil
}

// Reindex перечитывает названные книги заново.
//
// Нужен, когда изменились правила разбора: книга не менялась, и доливка её
// пропустит, а куски в индексе устарели.
func Reindex(stdout io.Writer, cfg *config.Config, name string, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("укажите пути к книгам: ollchat --kb-reindex %s /путь/к/книге", name)
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opt := kb.IndexOpts{Workers: cfg.KB.Workers, MaxBytes: int64(cfg.KB.MaxBookMB) * 1024 * 1024}
	pl := progressPrinter()
	defer pl.stop()
	res, err := coll.Reindex(ctx, paths, opt, pl.update)
	pl.stop()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "коллекция %s: перечитано книг %d, кусков %d, за %s\n",
		name, res.Added, res.Chunks, res.Elapsed.Round(time.Second))
	if res.Scans+res.Errors > 0 {
		fmt.Fprintf(stdout, "  сканов %d, со сбоями %d\n", res.Scans, res.Errors)
	}
	fmt.Fprintln(stdout, "  прежние куски этих книг помечены удалёнными; место освободит /kb merge,")
	fmt.Fprintln(stdout, "  но его нельзя запускать, пока по коллекции собран граф понятий")
	return nil
}

// Rebase переписывает корень коллекции: старый путь к книгам на новый.
//
// Нужна при переносе коллекции на другую машину, где библиотека лежит иначе.
// Поиск, смыслы и граф переносятся сами — они путей не содержат. А вот реестр
// книг содержит, и без правки `--kb-sync` не узнает ни одной книги
// и проиндексирует библиотеку заново, удвоив коллекцию.
func Rebase(stdout io.Writer, cfg *config.Config, name, from, to string, dry bool) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	coll, err := base.Open(name)
	if err != nil {
		return err
	}

	// Пустой старый корень берётся из паспорта, но только когда он там один:
	// угадывать за человека, какой из нескольких он имел в виду, нельзя.
	if strings.TrimSpace(from) == "" {
		roots := coll.Roots()
		switch len(roots) {
		case 0:
			return fmt.Errorf("у коллекции %s нет записанных корней — укажите --kb-rebase-from", name)
		case 1:
			from = roots[0]
			fmt.Fprintf(stdout, "старый корень взят из паспорта коллекции: %s\n", from)
		default:
			return fmt.Errorf("у коллекции %s несколько корней:\n  %s\nукажите нужный через --kb-rebase-from",
				name, strings.Join(roots, "\n  "))
		}
	}

	res, err := coll.Rebase(from, to, dry)
	if err != nil {
		return err
	}

	if dry {
		fmt.Fprintf(stdout, "сухой прогон, ничего не записано\n")
	}
	fmt.Fprintf(stdout, "коллекция %s: книг с новым путём %d, корней %d\n", name, res.Books, res.Roots)
	if res.Skipped > 0 {
		fmt.Fprintf(stdout, "  не под старым корнем и потому не тронуто: %d\n", res.Skipped)
	}
	if len(res.Missing) > 0 {
		fmt.Fprintf(stdout, "  по новому пути не нашлось (показаны первые %d):\n", len(res.Missing))
		for _, p := range res.Missing {
			fmt.Fprintf(stdout, "    %s\n", p)
		}
		fmt.Fprintln(stdout, "  поиск, векторы(смыслы) и граф от этого не пострадают — файлы нужны только доливке")
	}
	if dry {
		fmt.Fprintln(stdout, "применить: та же команда без --kb-dry-run")
	}
	return nil
}

// Doctor печатает проверку коллекции.
//
// **Зачем в командной строке.** Проверку зовут тогда, когда «поиск что-то
// перестал находить» — а это чаще всего происходит на сервере, где интерфейса
// нет вовсе. Отчёт тот же самый, что у `/kb doctor`: одна реализация,
// `kb.Doctor`.
func Doctor(stdout io.Writer, cfg *config.Config, name string, quick bool) error {
	base, err := kb.OpenBase(cfg.KB.Dir)
	if err != nil {
		return err
	}
	defer base.Close()

	names := []string{name}
	if name == "all" {
		if names, err = base.Names(); err != nil {
			return err
		}
		if len(names) == 0 {
			return fmt.Errorf("в базе знаний нет коллекций")
		}
	}
	for i, n := range names {
		coll, err := base.Open(n)
		if err != nil {
			return err
		}
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		// Строка хода на всю проверку, включая открытие графа: оно и есть
		// самая долгая её часть, а происходит **до** входа в kb.Doctor —
		// поставь её внутрь, и первые десять секунд программа молчала бы
		// ровно так же, как раньше.
		line := newStageLine(isTTY(os.Stderr))

		// Сверка по содержимому читает файлы целиком — на большой библиотеке
		// это секунды. Ключ --kb-quick её снимает: бывает нужен быстрый ответ
		// «книги на месте?», а не полная проверка.
		// Граф открывается только ради одного вопроса — «разобрана ли книга»,
		// — и потому по требованию: на большой коллекции это секунды и гигабайт.
		var inGraph kb.InGraph
		if !quick {
			line.step("открываю граф", 0, 0)
			if g, err := graph.Open(coll.Dir(), coll.ChunkCount(), cfg.Graph.Rules()); err == nil {
				inGraph = g.CoversDoc
				defer g.Close()
			}
		}
		report := kb.Doctor(coll, kb.DoctorOpts{
			Deep:    !quick,
			InGraph: inGraph,
			// Цвет — только в терминал. Перенаправленный в файл отчёт должен
			// оставаться текстом: его читают глазами и кладут в письмо.
			Paint: paintIf(isTTY(os.Stdout)),
			Step:  line.step,
		})
		line.stop()
		fmt.Fprintln(stdout, report)
	}
	return nil
}

// stageLine — строка хода работы для проверки коллекции.
//
// **Зачем отдельная от progressLine.** Та ведёт индексацию: у неё есть куски,
// скорость и остаток по книгам. У проверки шаги разной природы — открытие графа
// длится десяток секунд и о своём ходе не сообщает вовсе, а сверка по содержимому
// считается в книгах. Натягивать одну строку на оба случая значило бы показывать
// «0/0 · 0/с» там, где считать нечего.
//
// **Зачем вообще.** Проверка на живой библиотеке идёт около семнадцати секунд,
// из них большая часть — открытие графа. Всё это время программа молчит,
// и отличить её от зависшей нельзя ничем.
type stageLine struct {
	mu      sync.Mutex
	start   time.Time
	stage   string
	done    int
	total   int
	frame   int
	stopped bool
	timer   *time.Ticker
	on      bool // рисовать ли вообще: не в терминале строка хода — мусор в файле
}

func newStageLine(on bool) *stageLine {
	s := &stageLine{start: time.Now(), on: on}
	if !on {
		return s
	}
	s.timer = time.NewTicker(200 * time.Millisecond)
	go func() {
		for range s.timer.C {
			s.mu.Lock()
			stopped := s.stopped
			s.frame++
			s.mu.Unlock()
			if stopped {
				return
			}
			s.draw()
		}
	}()
	return s
}

// step отмечает шаг. total = 0 — сколько всего, неизвестно.
func (s *stageLine) step(stage string, done, total int) {
	s.mu.Lock()
	s.stage, s.done, s.total = stage, done, total
	s.mu.Unlock()
	s.draw()
}

// stop убирает строку за собой.
//
// Именно убирает, а не оставляет: следом идёт отчёт, и недописанная строка хода
// прилипла бы к его первой строке.
func (s *stageLine) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	if s.timer != nil {
		s.timer.Stop()
	}
	if s.on {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
}

func (s *stageLine) draw() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.on || s.stopped || s.stage == "" {
		return
	}
	line := s.stage + " " + s.barFor() +
		fmt.Sprintf(" · %s", humanLeft(time.Since(s.start).Round(time.Second)))
	fmt.Fprintf(os.Stderr, "\r\033[K%s", line)
}

// barFor рисует полосу: обычную, когда известно всего, и бегущую, когда нет.
//
// **Бегущая полоса — не украшение.** Открытие графа занимает большую часть
// проверки и о своём ходе сказать ничего не может: это одно чтение файла на
// гигабайт. Замершая полоса «0%» на десять секунд читается как зависание —
// ровно то, от чего эта строка и заводилась. Движение говорит «работа идёт»,
// не обещая при этом знать, сколько осталось.
func (s *stageLine) barFor() string {
	if s.total > 0 {
		pct := 100 * s.done / s.total
		return bar(pct) + fmt.Sprintf(" %3d%% %d/%d", pct, s.done, s.total)
	}
	const width = 20
	pos := s.frame % (2*width - 2) // ход туда и обратно, без остановки на краях
	if pos >= width {
		pos = 2*width - 2 - pos
	}
	return "[" + strings.Repeat("░", pos) + "█" + strings.Repeat("░", width-pos-1) + "]"
}

// isTTY отвечает, смотрит ли человек в терминал.
//
// От этого зависят и цвет, и строка хода: `--kb-doctor books > файл.txt`
// должен давать чистый текст, а не управляющие последовательности.
//
// **Проверка настоящая, через ioctl, а не по типу файла.** `os.ModeCharDevice`
// выставлен и у `/dev/null`, а это ровно тот случай, когда терминала нет:
// `--kb-merge books < /dev/null` выглядел бы для программы разговором с
// человеком. Для цвета такая ошибка стоит мусора в файле, а для подтверждения
// необратимого действия — гораздо дороже. Поймано своим же тестом 30.08.2026.
func isTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// paintBook красит название книги светло-жёлтым.
//
// Цвет 229 из палитры ANSI 256 — тот же, что и в ленте диалога: отчёт один,
// и выглядеть в двух местах по-разному он не должен.
func paintBook(s string) string { return "\033[38;5;229m" + s + "\033[0m" }

// paintIf отдаёт кисть, только если красить есть куда.
func paintIf(color bool) kb.Painter {
	if !color {
		return nil
	}
	return paintBook
}

// Уплотнение коллекции — единственное необратимое действие над базой знаний.
//
// **Что оно делает.** Переписывает хранилище кусков заново, оставляя только те,
// что принадлежат живым книгам: текст помеченных удалёнными и куски книг,
// перечитанных заново, исчезают с диска, а сегменты сливаются в один.
// Место освобождается, поиск ускоряется на один проход.
//
// **Чего не вернуть.** Сквозная нумерация кусков меняется. Это ломает две вещи:
// внешние ссылки вида `books/12#37`, выданные раньше, и — главное — **граф
// понятий**, который на эти номера опирается. Граф после уплотнения не
// откроется вовсе, и собрать его заново стоит десятков часов работы видеокарты.
// Отменить нельзя ничем: прежнего хранилища не остаётся.
//
// Отсюда два подтверждения подряд, а не одно. Одно нажатие `y` человек делает
// не глядя — на то оно и привычное. Здесь нужно написать слово, и написать
// его дважды, отвечая на два разных вопроса.

// mergeInfo — что даст и что отнимет уплотнение.
type mergeInfo struct {
	liveBooks   int
	liveChunks  int
	delBooks    int
	delChunks   int
	orphanChunk int // куски книг, перечитанных заново: записи нет, текст лежит
	physical    int // всего кусков в хранилище
	bytes       int64
	segments    int
	hasGraph    bool
	graph       graph.Meta // паспорт графа: читается даром, граф не открывается
}

func mergePreview(c *kb.Collection) mergeInfo {
	var mi mergeInfo
	for _, b := range c.LiveBooks() {
		mi.liveBooks++
		mi.liveChunks += b.Chunks
	}
	for _, b := range c.DeletedBooks() {
		mi.delBooks++
		mi.delChunks += b.Chunks
	}
	st := c.Stats()
	mi.physical, mi.bytes, mi.segments = st.Chunks, st.Bytes, st.Segments
	// Ничьи куски: хранилище знает о них, реестр — нет. Появляются, когда книгу
	// перечитывают заново: запись заменяется по пути, а прежний текст остаётся
	// лежать до уплотнения. Считать их отдельно важно — обычно их не меньше,
	// чем у помеченных удалёнными, и без них итог «сколько освободится» врёт.
	if o := mi.physical - mi.liveChunks - mi.delChunks; o > 0 {
		mi.orphanChunk = o
	}
	mi.hasGraph = c.HasGraph()
	if mi.hasGraph {
		// Ошибку глотаем намеренно: паспорт не прочитался — предупреждение
		// станет короче, но не пропадёт. Отказываться уплотнять из-за этого
		// не за что, а падать посреди объяснения — тем более.
		mi.graph, _ = graph.Stat(filepath.Join(c.Dir(), "graph"))
	}
	return mi
}

// freed — сколько кусков уйдёт с диска.
func (mi mergeInfo) freed() int { return mi.delChunks + mi.orphanChunk }

// explain рассказывает, к чему приведёт команда.
func (mi mergeInfo) explain(name string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Уплотнение коллекции %s\n\n", name)

	fmt.Fprintf(&b, "Что будет сделано:\n")
	fmt.Fprintf(&b, "  · хранилище кусков переписывается заново, остаются только живые книги: %d\n", mi.liveBooks)
	if mi.delBooks > 0 {
		fmt.Fprintf(&b, "  · текст %d книг, помеченных удалёнными, стирается с диска (%d кусков)\n",
			mi.delBooks, mi.delChunks)
	}
	if mi.orphanChunk > 0 {
		fmt.Fprintf(&b, "  · стирается %d кусков книг, перечитанных заново: записи о них уже нет\n",
			mi.orphanChunk)
	}
	if mi.segments > 1 {
		// **Без обещания ускорить поиск.** Замер 30.08.2026 на копии этой самой
		// библиотеки: 18 сегментов — 28.37 мс на запрос, тот же корпус в одном
		// сегменте — 28.68 мс. Разницы нет. Прежний текст обещал «один проход
		// вместо восемнадцати», и это звучало как выигрыш, которого не измерил
		// никто.
		fmt.Fprintf(&b, "  · %d сегментов сливаются в один (на скорости поиска это,\n"+
			"    по замеру на этой библиотеке, не сказывается)\n", mi.segments)
	}
	if mi.physical > 0 && mi.freed() > 0 {
		share := float64(mi.freed()) / float64(mi.physical)
		fmt.Fprintf(&b, "  · освободится примерно %.1f МБ из %.1f МБ\n",
			float64(mi.bytes)*share/1e6, float64(mi.bytes)/1e6)
	}

	b.WriteString("\nЧего нельзя будет вернуть:\n")
	b.WriteString("  · прежнего хранилища не остаётся — отмены нет ни у команды, ни у программы;\n")
	b.WriteString("  · сквозная нумерация кусков меняется, и выданные раньше ссылки\n")
	b.WriteString("    вида «" + name + "/12#37» станут указывать не туда;\n")
	if mi.hasGraph {
		b.WriteString("  · ГРАФ ПОНЯТИЙ ПЕРЕСТАНЕТ ОТКРЫВАТЬСЯ. Он опирается на эту нумерацию.\n")
		// Числа, а не слово «дорого». «Десятки часов» человек пролистывает,
		// «116801 понятие и 587644 связи» — нет. Берутся даром из паспорта
		// графа: открывать его ради предупреждения значило бы ждать 16 секунд.
		if g := mi.graph; g.Entities > 0 {
			fmt.Fprintf(&b, "    Потеряется: %d понятий, %d связей, %d упоминаний.\n",
				g.Entities, g.Edges, g.Mentions)
			fmt.Fprintf(&b, "    Собрать заново — прогнать %d кусков через %s:\n",
				g.Covered, g.Model)
			if g.BuildSeconds > 0 {
				fmt.Fprintf(&b, "    на это уже ушло %s работы видеокарты, столько же уйдёт снова.\n",
					humanLeft(time.Duration(g.BuildSeconds)*time.Second))
			} else {
				b.WriteString("    ровно столько же работы видеокарты, сколько уже потрачено.\n")
			}
		} else {
			b.WriteString("    Собрать его заново — десятки часов работы видеокарты.\n")
		}
	}
	return b.String()
}

// confirmMerge спрашивает дважды. Ответ — слово ДА, и только оно.
//
// **Почему слово, а не клавиша.** Согласие ценой одного нажатия человек даёт
// не читая: рука знает, где `y`, раньше, чем глаз дочитал вопрос. Слово надо
// набрать, а набрав — на секунду задуматься, что набираешь.
//
// **Почему дважды и разными вопросами.** Второй вопрос спрашивает о другом:
// первый — про коллекцию, второй — про то, что дороже всего, про граф.
// Два одинаковых вопроса подряд человек проходит на одном движении.
func confirmMerge(name string, mi mergeInfo, force, yes bool) error {
	// Отказ по графу остаётся отказом: подтверждение словом его не снимает.
	// Снимает только явный ключ — он и означает «я знаю, что теряю».
	if mi.hasGraph && !force {
		return fmt.Errorf("по коллекции %s собран граф понятий, и уплотнение сделает его нечитаемым;\n"+
			"если граф нужен — уплотнять нельзя; если нет — повторите с --kb-merge-force", name)
	}
	if yes {
		fmt.Fprintln(os.Stdout, "\n--kb-yes: подтверждения пропущены.")
		return nil
	}
	if !isTTY(os.Stdin) {
		return fmt.Errorf("уплотнение требует подтверждения, а ввод не с терминала;\n" +
			"в скрипте добавьте --kb-yes, если действительно этого хотите")
	}

	in := bufio.NewReader(os.Stdin)
	ask := func(question string) error {
		fmt.Fprintf(os.Stdout, "\n%s\nНапишите ДА (заглавными) и нажмите Enter: ", question)
		line, err := in.ReadString('\n')
		if err != nil {
			return fmt.Errorf("подтверждение не получено: %w", err)
		}
		if strings.TrimSpace(line) != "ДА" {
			return fmt.Errorf("уплотнение отменено: коллекция осталась прежней")
		}
		return nil
	}

	if err := ask(fmt.Sprintf(
		"Уплотнить коллекцию %s и стереть с диска %s без возможности вернуть?",
		name, chunksWord(mi.freed()))); err != nil {
		return err
	}
	second := "Ссылки на куски, выданные раньше, станут неверными. Подтверждаете второй раз?"
	if mi.hasGraph {
		second = "Граф понятий (" + name + ") после этого не откроется и потребует пересборки.\n" +
			"Это десятки часов работы видеокарты. Подтверждаете второй раз?"
	}
	return ask(second)
}

// chunksWord склоняет «N кусков»: вопрос читает человек, и «21 кусков»
// в нём выглядит небрежностью там, где нужна собранность.
func chunksWord(n int) string {
	word := "кусков"
	switch {
	case n%10 == 1 && n%100 != 11:
		word = "кусок"
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		word = "куска"
	}
	return fmt.Sprintf("%d %s", n, word)
}
