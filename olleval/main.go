// Программа olleval гоняет модели Ollama по наборам задач и записывает, как
// каждая справилась: балл, скорость, цену ответа в токенах, сбои и странности.
//
// Сама она ничего не оценивает на глаз. Её работа — довести каждую задачу до
// каждой модели одинаковым образом, сохранить сырьё целиком и прогнать
// машинную проверку там, где она возможна. Открытые ответы помечаются
// «нужен разбор человеком»: балл по ним ставится утром, по чек-листу.
//
// Запускать её надо на самом стенде, из tmux: ночь длинная, ssh рвётся.
// Порядок ночи и правила прогона — в AItestingPlan.md.
//
//	olleval guard                     проверить, свободна ли видеокарта
//	olleval pending                   осталась ли работа на эту ночь
//	olleval night                     какую ночь продолжать
//	olleval models                    список моделей сервера и кто участвует
//	olleval run --suite go --until 06:45
//	olleval report --night 2026-08-22
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

const version = "olleval 0.3.0"

// errSilent — «ответ отрицательный, объяснять нечего». Нужен для проверок
// вида `olleval window --quiet` внутри скриптов: они смотрят на код возврата,
// а лишняя строка в журнале ночи только мешает читать.
var errSilent = errors.New("")

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "guard":
		err = cmdGuard(ctx, os.Args[2:])
	case "models":
		err = cmdModels(ctx, os.Args[2:])
	case "run":
		err = cmdRun(ctx, os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "config":
		err = cmdConfig(os.Args[2:])
	case "timers":
		err = cmdTimers(os.Args[2:])
	case "night":
		err = cmdNight(ctx, os.Args[2:])
	case "pending":
		err = cmdPending(ctx, os.Args[2:])
	case "window":
		err = cmdWindow(os.Args[2:])
	case "running":
		err = cmdRunning(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		if !errors.Is(err, errSilent) {
			fmt.Fprintln(os.Stderr, "ошибка: "+err.Error())
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `olleval — прогон моделей Ollama по наборам задач

  olleval guard    [флаги]   проверить, свободна ли видеокарта (код 1 — занята)
  olleval models   [флаги]   показать модели сервера и кто участвует в прогоне
  olleval run      [флаги]   прогнать наборы задач по всем моделям
  olleval report   [флаги]   сводка по прогону из index.jsonl
  olleval config   [флаги]   показать настройки (--init создать, --get ключ)
  olleval timers   [флаги]   напечатать юниты systemd по расписанию из настроек
  olleval window   [флаги]   идёт ли сейчас окно прогонов (код 1 — нет)
  olleval pending  [флаги]   сколько попыток ночи не сделано (код 1 — работы нет)
  olleval night    [флаги]   какую ночь брать: продолжить незаконченную или завести новую
  olleval running  [флаги]   жив ли прогон прямо сейчас (код 1 — нет)

Общие флаги: --url, --root. Подробности: olleval <команда> --help
`)
}

// commonFlags — то, что нужно всем подкомандам. Умолчания флагов берутся
// из конфига: расписание и правила проверки занятости живут там, а не в коде.
type commonFlags struct {
	url  *string
	root *string
	cfg  Config
	fs   *flag.FlagSet
}

func newFlags(name string, args []string) (commonFlags, error) {
	cfg, err := LoadConfig(configPathFromArgs(args))
	if err != nil {
		return commonFlags{}, err
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.String("config", "", "путь к файлу настроек (по умолчанию <root>/olleval.toml)")
	return commonFlags{
		url:  fs.String("url", cfg.URL, "адрес Ollama"),
		root: fs.String("root", cfg.Root, "корень данных прогонов"),
		cfg:  cfg,
		fs:   fs,
	}, nil
}

// configPath — какой файл настроек действует: указанный флагом или тот,
// что лежит в корне данных.
func configPath(root string, args []string) string {
	if p := configPathFromArgs(args); p != "" {
		return p
	}
	return ConfigPath(root)
}

// configPathFromArgs достаёт --config до разбора флагов: сам конфиг нужен
// раньше, чем определены флаги, — из него берутся их умолчания.
func configPathFromArgs(args []string) string {
	for i, a := range args {
		switch {
		case a == "--config" || a == "-config":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(a, "--config="):
			return strings.TrimPrefix(a, "--config=")
		case strings.HasPrefix(a, "-config="):
			return strings.TrimPrefix(a, "-config=")
		}
	}
	return ""
}

func defaultRoot() string {
	if v := os.Getenv("OLLEVAL_ROOT"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "ollevals"
	}
	return filepath.Join(home, "ollevals")
}

func (c commonFlags) client() *ollama.Client {
	return ollama.New(*c.url, 60*time.Second, 30*time.Minute, nil)
}

// ── guard ────────────────────────────────────────────────────────────────────

func cmdGuard(ctx context.Context, args []string) error {
	f, err := newFlags("guard", args)
	if err != nil {
		return err
	}
	need := f.fs.Int("free-checks", f.cfg.Guard.FreeChecks, "сколько свободных проверок подряд считать за «свободна»")
	poll := f.fs.Duration("poll", f.cfg.Guard.Poll.Get(time.Minute), "как часто проверять")
	wait := f.fs.Duration("wait", 0, "сколько всего ждать освобождения (0 — не ждать)")
	startService := f.fs.Bool("start-service", false, "поднять погашенную службу, если карта давно свободна")
	quiet := f.fs.Bool("quiet", false, "молча, только код возврата")
	_ = f.fs.Parse(args)

	g := NewGuard(f.client(), f.cfg.Guard, *f.root)
	// Порог загрузки берётся из промежутка расписания: ночью и в выходной
	// день терпимость к чужой работе разная.
	g.Limit = ScheduleGPULimit(f.cfg.Schedule, f.cfg.Guard.BusyUtilPercent)
	log := func(s string) {
		if !*quiet {
			fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), s)
		}
	}
	// Нулевое ожидание означает «проверить и ответить», а не «ждать вечно»:
	// без этого `guard` без флагов молча висел бы на занятой карте.
	giveUp := time.Now()
	if *wait > 0 {
		giveUp = giveUp.Add(*wait)
	}
	rep, _ := g.WaitFree(ctx, *need, *poll, giveUp, log)
	if rep.NeedServiceStart && *startService {
		doc := NewDoctor(f.client(), HealthCfg{RestartWait: Duration(5 * time.Minute)},
			func(format string, args ...any) { log(fmt.Sprintf(format, args...)) })
		if err := doc.StartService(ctx, f.cfg.Guard.Service); err != nil {
			return err
		}
	}
	if !*quiet {
		for _, note := range rep.Notes {
			fmt.Println("к сведению: " + note)
		}
		if rep.Free {
			fmt.Printf("карта свободна — прогон можно начинать (загрузка %d%% при пороге %d%%)\n",
				rep.GPUUtil, rep.UtilLimit)
		} else {
			for _, b := range rep.Blocking {
				fmt.Println("занято: " + b)
			}
		}
	}
	if !rep.Free {
		return errors.New("видеокарта занята, прогон не начинаем")
	}
	return nil
}

// ── models ───────────────────────────────────────────────────────────────────

func cmdModels(ctx context.Context, args []string) error {
	f, err := newFlags("models", args)
	if err != nil {
		return err
	}
	exclude := f.fs.String("exclude", strings.Join(f.cfg.Run.Exclude, ","), "модели через запятую, которые не участвуют")
	_ = f.fs.Parse(args)

	tags, tagsErr := f.client().Tags(ctx)
	if tagsErr != nil {
		return tagsErr
	}
	cards := SelectModels(tags, strings.Split(*exclude, ","))
	for _, c := range cards {
		status := "участвует"
		if c.Skipped != "" {
			status = "пропуск: " + c.Skipped
		}
		fmt.Printf("%-32s %-8s %-8s %6.1f ГиБ  %-28s %s\n",
			c.Name, c.ParameterSiz, c.Quantization, c.SizeGiB,
			strings.Join(c.Capabilities, ","), status)
	}
	return nil
}

// ── run ──────────────────────────────────────────────────────────────────────

func cmdRun(ctx context.Context, args []string) error {
	f, err := newFlags("run", args)
	if err != nil {
		return err
	}
	c := f.cfg
	suiteNames := f.fs.String("suite", c.Run.Suites, "наборы через запятую или all")
	night := f.fs.String("night", "", "имя ночи (по умолчанию сегодняшняя дата)")
	until := f.fs.String("until", "", "до какого времени работать, ЧЧ:ММ (пусто — до конца текущего окна)")
	tz := f.fs.String("tz", c.Schedule.Timezone, "часовой пояс для --until: стенд живёт по UTC")
	forDur := f.fs.Duration("for", 0, "сколько работать, если не задан --until")
	repeats := f.fs.Int("repeats", c.Run.Repeats, "повторов каждой задачи")
	numCtx := f.fs.Int("num-ctx", c.Run.NumCtx, "окно контекста для всех моделей")
	retries := f.fs.Int("retries", c.Run.Retries, "повторов обмена, сорванного до первого куска ответа")
	timeout := f.fs.Duration("timeout", c.Run.Timeout.Get(20*time.Minute), "предел на одну генерацию")
	keep := f.fs.String("keep-alive", c.Run.KeepAlive, "сколько держать модель в памяти внутри блока")
	exclude := f.fs.String("exclude", strings.Join(c.Run.Exclude, ","), "модели через запятую, которые не участвуют")
	models := f.fs.String("models", "", "прогнать только эти модели (через запятую)")
	freeChecks := f.fs.Int("free-checks", c.Guard.FreeChecks, "сколько свободных проверок подряд нужно для старта")
	poll := f.fs.Duration("poll", c.Guard.Poll.Get(time.Minute), "как часто проверять занятость карты")
	waitUntil := f.fs.String("wait-until", "", "до какого времени ждать освобождения карты, ЧЧ:ММ")
	skipGuard := f.fs.Bool("skip-guard", false, "не проверять занятость карты (только для отладки)")
	note := f.fs.String("note", "", "пометка в паспорт ночи")
	_ = f.fs.Parse(args)

	client := f.client()
	deadline, err := runDeadline(*until, *tz, *forDur, c.Schedule)
	if err != nil {
		return err
	}

	waitLog := func(s string) { fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), s) }
	if !*skipGuard {
		giveUp, waitErr := waitDeadline(*waitUntil, *tz, deadline, c.Schedule.Wait.Get(time.Hour))
		if waitErr != nil {
			return waitErr
		}
		guard := NewGuard(client, c.Guard, *f.root)
		guard.Limit = ScheduleGPULimit(c.Schedule, c.Guard.BusyUtilPercent)
		rep, ok := guard.WaitFree(ctx, *freeChecks, *poll, giveUp, waitLog)
		for _, n := range rep.Notes {
			waitLog("к сведению: " + n)
		}
		if !ok {
			return errors.New("видеокарта занята — прогон не начинается (правило номер один)")
		}
		// Службу гасят под обучение и забывают включить обратно. Карта простояла
		// свободной достаточно долго — поднимаем сами, иначе стенд пропадёт зря.
		if rep.NeedServiceStart {
			doc := NewDoctor(client, c.Health, func(format string, args ...any) {
				waitLog(fmt.Sprintf(format, args...))
			})
			if err := doc.StartService(ctx, c.Guard.Service); err != nil {
				return fmt.Errorf("не удалось поднять службу: %w", err)
			}
		}
	}

	// Забытая в памяти модель прогон больше не запрещает — значит, снять её
	// с карты надо самим: иначе первая модель ночи не поместится целиком
	// и поедет в оперативную память, а замер превратится в замер диска.
	unloadLoaded(ctx, client, waitLog)

	name := *night
	if name == "" {
		name = time.Now().Format("2006-01-02")
	}
	store, err := NewStore(*f.root, name)
	if err != nil {
		return err
	}

	suites, err := pickSuites(store.SuitesDir(), *suiteNames)
	if err != nil {
		return err
	}

	tags, err := client.Tags(ctx)
	if err != nil {
		return err
	}
	cards := SelectModels(tags, strings.Split(*exclude, ","))
	cards = keepOnly(cards, *models)

	srvVersion, _ := client.Version(ctx)
	host, _ := os.Hostname()
	suiteList := make([]string, 0, len(suites))
	for _, s := range suites {
		suiteList = append(suiteList, s.Name)
	}
	passport := &Passport{
		Night: name, StartedAt: time.Now(), Host: host, OllamaURL: *f.url,
		OllamaVersion: srvVersion, NumCtx: *numCtx, Repeats: *repeats,
		Deadline: deadline, Suites: suiteList, Models: cards, Note: *note,
	}
	if err := store.SavePassport(passport); err != nil {
		return err
	}

	logf := func(format string, args ...any) {
		fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	}
	logf("ночь %s, наборов %d, моделей %d, повторов %d, окно %d",
		name, len(suites), countActive(cards), *repeats, *numCtx)
	if !deadline.IsZero() {
		logf("работаю до %s", deadline.Format("15:04:05 MST"))
	}

	n := &Night{
		Runner: &Runner{
			Client: client, Store: store, Fixtures: store.FixturesDir(),
			NumCtx: *numCtx, Retries: *retries, Timeout: *timeout, KeepFor: *keep,
		},
		Verifier: NewVerifier(store.FixturesDir(), c.Verify),
		Doctor:   NewDoctor(client, c.Health, logf),
		Store:    store, Client: client, Suites: suites, Models: cards,
		Repeats: *repeats, Deadline: deadline, Log: logf,
	}
	// За расписанием следим, только если предел взят из него же: при ручном
	// прогоне с явным --until человек сам знает, чего хочет.
	if *until == "" && *forDur == 0 {
		n.Watch = WatchSchedule(configPath(*f.root, args))
	}

	done, runErr := n.Run(ctx)
	_ = ClearHeartbeat(store.Root)
	passport.FinishedAt = time.Now()
	if err := store.SavePassport(passport); err != nil {
		return err
	}
	logf("готово: попыток за этот заход %d, сырьё в %s", done, store.NightDir())
	return runErr
}

// parseDeadline считает, до какого момента работать. Час задаётся в часовом
// поясе владельца стенда, а не сервера: стенд живёт по UTC, и «до 06:45»
// без пояса означало бы 09:45 по Москве — на три часа в чужое утро.
func parseDeadline(until, tz string, forDur time.Duration) (time.Time, error) {
	if until == "" {
		if forDur > 0 {
			return time.Now().Add(forDur), nil
		}
		return time.Time{}, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Time{}, fmt.Errorf("часовой пояс %q: %w", tz, err)
	}
	t, err := time.ParseInLocation("15:04", until, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("время %q: нужен вид ЧЧ:ММ", until)
	}
	now := time.Now().In(loc)
	deadline := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	if !deadline.After(now) {
		deadline = deadline.AddDate(0, 0, 1) // «до 06:45», начатое вечером, — это завтра
	}
	return deadline, nil
}

// waitDeadline считает, до какого момента ждать освобождения карты.
// По умолчанию — до конца самой ночи: если карта освободилась в шесть утра,
// прогнать хоть один набор всё равно лучше, чем ничего, а недобранное
// докатится следующей ночью.
// runDeadline решает, до какого момента раздавать задачи.
//
// Без явного --until предел берётся из текущего окна: по будням это 06:45,
// а в выходные — конец промежутка, заданного в настройках. Так одно и то же
// расписание годится и для ночи, и для суток субботы.
func runDeadline(until, tz string, forDur time.Duration, sch Schedule) (time.Time, error) {
	if until != "" || forDur > 0 {
		return parseDeadline(until, tz, forDur)
	}
	if w, ok, err := CurrentWindow(time.Now(), sch); err != nil {
		return time.Time{}, err
	} else if ok {
		return w.Deadline, nil
	}
	// Вне окна предел всё равно нужен: прогон могли запустить руками.
	return parseDeadline(sch.Until, tz, 0)
}

func waitDeadline(waitUntil, tz string, runDeadline time.Time, fallback time.Duration) (time.Time, error) {
	if waitUntil != "" {
		return parseDeadline(waitUntil, tz, 0)
	}
	if !runDeadline.IsZero() {
		return runDeadline, nil
	}
	return time.Now().Add(fallback), nil
}

func pickSuites(dir, names string) ([]*Suite, error) {
	all, err := LoadSuites(dir)
	if err != nil {
		return nil, fmt.Errorf("наборы задач в %s: %w", dir, err)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("в %s нет ни одного набора задач", dir)
	}
	if names == "" || names == "all" {
		return all, nil
	}
	want := make(map[string]bool)
	for _, n := range strings.Split(names, ",") {
		want[strings.TrimSpace(n)] = true
	}
	out := make([]*Suite, 0, len(want))
	for _, s := range all {
		if want[s.Name] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("наборов %q нет в %s", names, dir)
	}
	return out, nil
}

func keepOnly(cards []ModelCard, list string) []ModelCard {
	if strings.TrimSpace(list) == "" {
		return cards
	}
	want := make(map[string]bool)
	for _, n := range strings.Split(list, ",") {
		want[strings.TrimSpace(n)] = true
	}
	for i := range cards {
		if !want[cards[i].Name] && cards[i].Skipped == "" {
			cards[i].Skipped = "не в списке --models"
		}
	}
	return cards
}

func countActive(cards []ModelCard) int {
	var n int
	for _, c := range cards {
		if c.Skipped == "" {
			n++
		}
	}
	return n
}

// ── night ────────────────────────────────────────────────────────────────────

// cmdNight печатает имя ночи, с которой работать.
//
// Нужна скрипту обвязки: без неё он брал имя ночи как сегодняшнюю дату
// и каждую полночь начинал набор заново, бросая позавчерашний недоделанным.
func cmdNight(ctx context.Context, args []string) error {
	f, err := newFlags("night", args)
	if err != nil {
		return err
	}
	c := f.cfg
	suiteNames := f.fs.String("suite", c.Run.Suites, "наборы через запятую или all")
	repeats := f.fs.Int("repeats", c.Run.Repeats, "повторов каждой задачи")
	exclude := f.fs.String("exclude", strings.Join(c.Run.Exclude, ","), "модели через запятую, которые не участвуют")
	models := f.fs.String("models", "", "считать только эти модели (через запятую)")
	maxAge := f.fs.Duration("max-age", c.Run.CarryOver.Get(3*24*time.Hour),
		"насколько старую ночь ещё имеет смысл докатывать")
	verbose := f.fs.Bool("verbose", false, "объяснить решение словами")
	_ = f.fs.Parse(args)

	loc, err := time.LoadLocation(c.Schedule.Timezone)
	if err != nil {
		return err
	}
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")

	count := pendingCounter(ctx, *f.root, f.client(), *suiteNames, *exclude, *models, *repeats)
	choice, err := ResolveNight(*f.root, today, *maxAge, now, count)
	if err != nil {
		return err
	}
	if *verbose {
		if choice.Carried {
			fmt.Printf("продолжаю ночь %s: в ней осталось %d попыток\n", choice.Name, choice.Pending)
		} else if choice.Previous != "" {
			fmt.Printf("ночь %s доделана — завожу новую %s\n", choice.Previous, choice.Name)
		} else {
			fmt.Printf("завожу ночь %s\n", choice.Name)
		}
		return nil
	}
	fmt.Println(choice.Name)
	return nil
}

// ── pending ──────────────────────────────────────────────────────────────────

// cmdPending отвечает, осталась ли работа на эту ночь. Нужна скрипту ночи:
// он спрашивает **до** закрытия сервера. Ночь доделывает всю работу к утру,
// а окно выходного дня длится сутки — без этой проверки тик каждые четверть
// часа закрывал бы Ollama на localhost, находил ноль работы и открывал обратно.
//
// Код возврата 1 означает «делать нечего» — так же, как у `window` и `running`.
func cmdPending(ctx context.Context, args []string) error {
	f, err := newFlags("pending", args)
	if err != nil {
		return err
	}
	c := f.cfg
	suiteNames := f.fs.String("suite", c.Run.Suites, "наборы через запятую или all")
	night := f.fs.String("night", "", "имя ночи (по умолчанию сегодняшняя дата)")
	repeats := f.fs.Int("repeats", c.Run.Repeats, "повторов каждой задачи")
	exclude := f.fs.String("exclude", strings.Join(c.Run.Exclude, ","), "модели через запятую, которые не участвуют")
	models := f.fs.String("models", "", "считать только эти модели (через запятую)")
	quiet := f.fs.Bool("quiet", false, "молча, только код возврата")
	_ = f.fs.Parse(args)

	name := *night
	if name == "" {
		name = time.Now().Format("2006-01-02")
	}
	store, err := NewStore(*f.root, name)
	if err != nil {
		return err
	}
	suites, err := pickSuites(store.SuitesDir(), *suiteNames)
	if err != nil {
		return err
	}
	client := f.client()
	tags, err := client.Tags(ctx)
	if err != nil {
		return err
	}
	cards := keepOnly(SelectModels(tags, strings.Split(*exclude, ",")), *models)

	n := &Night{Store: store, Client: client, Suites: suites, Models: cards, Repeats: *repeats}
	left := n.Pending()
	if !*quiet {
		fmt.Println(left)
	}
	if left == 0 {
		return errSilent
	}
	return nil
}

// ── report ───────────────────────────────────────────────────────────────────

func cmdReport(args []string) error {
	f, err := newFlags("report", args)
	if err != nil {
		return err
	}
	night := f.fs.String("night", time.Now().Format("2006-01-02"), "какую ночь разбирать")
	_ = f.fs.Parse(args)

	store := &Store{Root: *f.root, Night: *night}
	recs, readErr := ReadIndex(filepath.Join(store.NightDir(), "index.jsonl"))
	if readErr != nil {
		return readErr
	}
	if len(recs) == 0 {
		return fmt.Errorf("в ночи %s нет ни одной попытки", *night)
	}

	sum := Summarize(recs)
	keys := make([]string, 0, len(sum))
	for k := range sum {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf("Ночь %s, попыток %d\n\n", *night, len(recs))
	fmt.Printf("%-32s %-12s %6s %6s %8s %8s %6s %8s\n",
		"модель", "набор", "задач", "балл", "с/задачу", "ток/с", "сбоев", "по врем.")
	for _, k := range keys {
		s := sum[k]
		fmt.Printf("%-32s %-12s %6d %6.2f %8.0f %8.1f %6d %8d\n",
			s.Model, s.Suite, s.Attempts, s.MeanScore, s.MedianSeconds, s.MedianTokPerSec,
			s.Errors, s.TimedOut)
	}
	if n := countReview(recs); n > 0 {
		fmt.Printf("\nждут разбора человеком: %d попыток\n", n)
	}
	return nil
}

func countReview(recs []Metrics) int {
	var n int
	for _, r := range recs {
		if r.NeedsReview {
			n++
		}
	}
	return n
}

// ── config ───────────────────────────────────────────────────────────────────

func cmdConfig(args []string) error {
	f, err := newFlags("config", args)
	if err != nil {
		return err
	}
	initFile := f.fs.Bool("init", false, "создать образец файла настроек")
	get := f.fs.String("get", "", "напечатать одно значение, например schedule.until")
	_ = f.fs.Parse(args)

	path := configPathFromArgs(args)
	if path == "" {
		path = ConfigPath(*f.root)
	}
	switch {
	case *initFile:
		if err := WriteConfigTemplate(path, *f.root); err != nil {
			return err
		}
		fmt.Println("файл настроек создан: " + path)
		return nil
	case *get != "":
		v, err := f.cfg.Get(*get)
		if err != nil {
			return err
		}
		fmt.Println(v)
		return nil
	}

	fmt.Println("# настройки из " + path)
	enc := toml.NewEncoder(os.Stdout)
	return enc.Encode(f.cfg)
}

// ── timers ───────────────────────────────────────────────────────────────────

// cmdTimers печатает юниты systemd с временем из настроек. Так расписание
// остаётся в одном месте: правится конфиг, юниты берутся отсюда.
func cmdTimers(args []string) error {
	f, err := newFlags("timers", args)
	if err != nil {
		return err
	}
	_ = f.fs.Parse(args)
	c := f.cfg

	var schedule strings.Builder
	for _, wd := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday,
		time.Thursday, time.Friday, time.Saturday, time.Sunday} {
		spans := c.Schedule.Days.For(wd)
		text := "прогонов нет"
		if len(spans) > 0 {
			text = strings.Join(spans, ", ")
		}
		fmt.Fprintf(&schedule, "#   %-12s %s\n", weekdays[wd]+":", text)
	}

	fmt.Printf(`# Расписание живёт в %[1]s, а не в таймерах: они лишь
# тикают раз в четверть часа, а скрипты сами решают, их ли сейчас время.
%[2]s#   пояс %[3]s, задачи прекращаются за %[4]s до конца окна.

# ── /etc/systemd/system/olleval-night.timer ──
[Unit]
Description=Тик прогонов olleval

[Timer]
OnCalendar=*-*-* *:00/15:00
Persistent=false
Unit=olleval-night.service

[Install]
WantedBy=timers.target

# ── /etc/systemd/system/olleval-restore.timer ──
[Unit]
Description=Проверка, не пора ли вернуть Ollama в общий доступ

[Timer]
OnCalendar=*-*-* *:07/15:00
Persistent=true
Unit=olleval-restore.service

[Install]
WantedBy=timers.target
`, ConfigPath(c.Root), schedule.String(), c.Schedule.Timezone,
		time.Duration(c.Schedule.Gap))
	return nil
}

// ── window ───────────────────────────────────────────────────────────────────

// cmdWindow отвечает, идёт ли сейчас ночное окно. Нужна скрипту ночи: он
// закрывает сервер на localhost, и запускать его среди рабочего дня нельзя.
func cmdWindow(args []string) error {
	f, err := newFlags("window", args)
	if err != nil {
		return err
	}
	quiet := f.fs.Bool("quiet", false, "молча, только код возврата")
	showEnd := f.fs.Bool("end", false, "напечатать конец текущего окна")
	showDeadline := f.fs.Bool("deadline", false, "напечатать, до какого момента раздаются задачи")
	showGPU := f.fs.Bool("gpu", false, "напечатать порог загрузки карты для текущего окна")
	_ = f.fs.Parse(args)

	sch := f.cfg.Schedule
	loc, err := time.LoadLocation(sch.Timezone)
	if err != nil {
		return err
	}
	w, in, err := CurrentWindow(time.Now(), sch)
	if err != nil {
		return err
	}
	switch {
	case !in:
		if *quiet {
			return errSilent
		}
		fmt.Printf("%s — вне окна прогонов (%s)\n", time.Now().In(loc).Format("15:04, Mon"), sch.Timezone)
		return errors.New("сейчас не окно прогонов")
	case *showEnd:
		fmt.Println(w.End.In(loc).Format(time.RFC3339))
	case *showDeadline:
		fmt.Println(w.Deadline.In(loc).Format(time.RFC3339))
	case *showGPU:
		fmt.Println(w.GPULimit(f.cfg.Guard.BusyUtilPercent))
	case !*quiet:
		fmt.Printf("%s — %s %s–%s (%s), задачи раздаются до %s, карта считается свободной до %d%% загрузки\n",
			time.Now().In(loc).Format("15:04, Mon"), w.Name(),
			w.Start.In(loc).Format("15:04"), w.End.In(loc).Format("15:04 Mon"),
			sch.Timezone, w.Deadline.In(loc).Format("15:04 Mon"),
			w.GPULimit(f.cfg.Guard.BusyUtilPercent))
	}
	return nil
}

// ── running ──────────────────────────────────────────────────────────────────

// cmdRunning отвечает, идёт ли прогон прямо сейчас. Нужна службе возврата
// сервера: закрытый стенд при живом прогоне — норма, при мёртвом — беда.
func cmdRunning(args []string) error {
	f, err := newFlags("running", args)
	if err != nil {
		return err
	}
	stale := f.fs.Duration("stale", 15*time.Minute, "после какого молчания считать прогон мёртвым")
	quiet := f.fs.Bool("quiet", false, "молча, только код возврата")
	_ = f.fs.Parse(args)

	hb, alive := LiveRun(*f.root, *stale)
	if !*quiet {
		switch {
		case alive:
			fmt.Printf("прогон идёт: ночь %s, модель %s, задача %s (отметка %s назад)\n",
				hb.Night, hb.Model, hb.Task, time.Since(hb.Updated).Round(time.Second))
		case hb.PID != 0:
			fmt.Printf("прогон мёртв: последняя отметка %s назад (ночь %s, модель %s)\n",
				time.Since(hb.Updated).Round(time.Second), hb.Night, hb.Model)
		default:
			fmt.Println("прогон не идёт")
		}
	}
	if !alive {
		if *quiet {
			return errSilent
		}
		return errors.New("живого прогона нет")
	}
	return nil
}
