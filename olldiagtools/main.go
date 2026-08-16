// Программа olldiag измеряет, сколько видеопамяти занимает модель Ollama
// в зависимости от размера контекстного окна, и считает, какое окно можно
// дать каждому из N одновременно работающих пользователей.
//
// Запускать её надо на самом сервере с Ollama: только там видно настоящее
// состояние карты через nvidia-smi. Через сеть доступен лишь /api/ps, а он
// не знает ни ёмкости карты, ни того, что заняли соседние процессы.
//
// Метод — нагрузочный, а не теоретический. Модель по очереди загружается
// с несколькими размерами окна, после каждой загрузки снимается занятая
// память, и по этим точкам находится зависимость. Так учитывается всё, что
// формула по метаданным не видит: доля слоёв с кэшем, буферы вычислений,
// накладные расходы CUDA.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/itpro/ollchat/internal/vram"
)

func main() {
	var (
		url       = flag.String("url", "http://127.0.0.1:11434", "адрес Ollama")
		models    = flag.String("models", "", "модели через запятую или all; пусто — показать список и выйти")
		points    = flag.String("points", "", "окна для замеров через запятую (по умолчанию подбираются сами)")
		users     = flag.String("users", "1,2,4,8", "для скольких одновременных пользователей считать таблицу")
		calc      = flag.Int("calc", 0, "посчитать окно для 1..N пользователей по всем моделям сервера")
		recalc    = flag.String("recalc", "", "пересчитать таблицы из готового профиля, без новых замеров")
		parallel  = flag.Int("parallel", envInt("OLLAMA_NUM_PARALLEL", 1), "слотов на модель (OLLAMA_NUM_PARALLEL)")
		budget    = flag.Float64("budget-mib", 0, "бюджет видеопамяти в МиБ (0 — определить по nvidia-smi)")
		reserve   = flag.Float64("reserve-mib", 512, "сколько МиБ оставить свободными про запас")
		loadWait  = flag.Duration("load-timeout", 15*time.Minute, "сколько ждать загрузки модели")
		keepAlive = flag.String("keep-alive", "2m", "keep_alive для замеров")
		outPath   = flag.String("out", "olldiag-profile.json", "куда записать профиль")
		verify    = flag.Bool("verify", false, "искать потолок настоящими загрузками, а не только расчётом")
		probes    = flag.Int("probes", 4, "сколько проб делать при поиске потолка")
		unload    = flag.Bool("unload", true, "выгружать модель после замеров")
	)
	flag.Parse()

	// Пересчёт из готового профиля: сервер не трогаем вовсе. Замеры — дорогая
	// часть, а таблицы из них выводятся заново за миллисекунды, так что
	// исправление формулы не требует повторных загрузок.
	if *recalc != "" {
		if err := runRecalc(*recalc, *calc, *users, *outPath); err != nil {
			fmt.Fprintln(os.Stderr, "ошибка: "+err.Error())
			os.Exit(1)
		}
		return
	}

	// -calc N — короткий путь к главному вопросу: сколько дать каждому из N.
	// Он подразумевает все модели, ряд пользователей от одного до N и поиск
	// потолка загрузками: без него числа были бы экстраполяцией.
	if *calc > 0 {
		if strings.TrimSpace(*models) == "" {
			*models = "all"
		}
		*users = userRange(*calc)
		*verify = true
	}

	if err := run(*url, *models, *points, *users, *parallel, *budget, *reserve,
		*loadWait, *keepAlive, *outPath, *verify, *probes, *unload); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка: "+err.Error())
		os.Exit(1)
	}
}

func run(url, modelsArg, pointsArg, usersArg string, parallel int,
	budgetOverride, reserve float64, loadWait time.Duration,
	keepAlive, outPath string, verify bool, probes int, unload bool) error {

	// Замеры длинные, поэтому Ctrl+C должен прерывать их сразу, а не после
	// загрузки очередной семидесятигигабайтной модели.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := newClient(url, loadWait+time.Minute)

	all, err := c.tags(ctx)
	if err != nil {
		return fmt.Errorf("список моделей: %w", err)
	}

	gpu := readGPU(ctx)
	printHeader(url, gpu, parallel)

	// Фон — что занято на карте до наших замеров. Расход модели считается
	// по карте, поэтому чужие процессы надо вычесть один раз и здесь.
	baseline := gpu.UsedMiB
	if running, err := c.ps(ctx); err == nil && len(running) > 0 {
		var loaded float64
		for _, r := range running {
			loaded += float64(r.SizeVRAM) / (1 << 20)
		}
		baseline -= loaded
		fmt.Printf("внимание: на карте уже загружены модели (%.0f МиБ по данным Ollama),\n", loaded)
		fmt.Printf("замеры будут точнее на свободной карте\n")
	}
	if baseline < 0 {
		baseline = 0
	}
	if baseline > 0 {
		fmt.Printf("фон (занято чужими процессами): %.0f МиБ\n", baseline)
	}

	if strings.TrimSpace(modelsArg) == "" {
		printModelList(all)
		return nil
	}
	chosen, err := pickModels(all, modelsArg)
	if err != nil {
		return err
	}

	userCounts, err := parseInts(usersArg)
	if err != nil {
		return fmt.Errorf("-users: %w", err)
	}

	profile := vram.Profile{
		Format:    vram.ProfileFormat,
		Host:      hostname(),
		OllamaURL: url,
		Parallel:  parallel,
		GPU:       gpu,
	}

	for _, m := range chosen {
		res, err := measureModel(ctx, c, m, pointsArg, loadWait, keepAlive, baseline)
		if err != nil {
			fmt.Printf("\n%s: %v\n", m.Name, err)
			continue
		}

		res.Budget = budgetFor(gpu, baseline, budgetOverride, reserve)
		res.Users = make([]vram.UserLimit, 0, len(userCounts))
		for _, u := range userCounts {
			res.Users = append(res.Users, vram.UserLimit{
				Users:      u,
				ContextMax: res.Fit.ContextForUsers(res.Budget.UsableMiB, u, res.MaxContext, 0),
			})
		}
		res.FullContextUsers = res.Fit.UsersAtFullContext(res.Budget.UsableMiB, res.MaxContext, 0)

		// При -verify предварительную таблицу не печатаем вовсе: она содержала
		// бы экстраполяцию и совет «проверьте флагом -verify», хотя проверка
		// уже идёт. Печатаем один раз — по итогам проверки.
		printModelReport(res, !verify)

		if verify {
			findCeiling(ctx, c, &res, probes, loadWait, keepAlive, baseline)
			printUsersTable(res)
		}
		if unload {
			_ = c.unload(ctx, m.Name)
		}
		profile.Models = append(profile.Models, res)
	}

	if len(profile.Models) == 0 {
		return fmt.Errorf("ни одной модели измерить не удалось")
	}
	printSummary(profile.Models)
	profile.GeneratedAt = time.Now().Format(time.RFC3339)
	if err := vram.WriteProfile(outPath, profile); err != nil {
		return err
	}
	fmt.Printf("\nПрофиль записан: %s\n", outPath)
	fmt.Println("Скопируйте его на машину с ollchat — команда /calc возьмёт замеры оттуда.")
	return nil
}

// ── Замер одной модели ───────────────────────────────────────────────────────

func measureModel(ctx context.Context, c *client, m modelInfo, pointsArg string,
	loadWait time.Duration, keepAlive string, baselineMiB float64) (vram.ModelResult, error) {

	res := vram.ModelResult{Name: m.Name, FileSizeMiB: float64(m.Size) / (1 << 20)}

	show, err := c.show(ctx, m.Name)
	if err != nil {
		return res, fmt.Errorf("сведения о модели: %w", err)
	}
	res.MaxContext = show.maxContext()
	res.Vision = show.hasVision()

	windows, err := samplePoints(pointsArg, res.MaxContext)
	if err != nil {
		return res, err
	}

	fmt.Printf("\n=== %s ===\n", m.Name)
	fmt.Printf("файл %.2f ГиБ, максимум окна %s\n", res.FileSizeMiB/1024, formatTokens(res.MaxContext))
	fmt.Println("замеры одного слота (один пользователь):")

	tb := newTable(
		column{"окно", alignRight},
		column{"занято на карте", alignRight},
		column{"по данным Ollama", alignRight},
		column{"размещение", alignLeft},
	)
	for _, w := range windows {
		s, err := sampleAt(ctx, c, m.Name, w, loadWait, keepAlive, baselineMiB)
		if err != nil {
			tb.row(formatTokens(w), "—", "—", "не удалось: "+err.Error())
			continue
		}
		place := "на GPU"
		if s.Spilled() {
			place = "ВЫТЕСНЕНИЕ"
		}
		tb.row(formatTokens(w), fmt.Sprintf("%.0f", s.UsageMiB),
			fmt.Sprintf("%.0f", s.PSTotalMiB), place)
		res.Samples = append(res.Samples, s)
	}
	fmt.Println(tb)

	fit, err := vram.FitSamples(res.Samples)
	if err != nil {
		return res, fmt.Errorf("подгонка: %w", err)
	}
	res.Fit = fit
	return res, nil
}

// sampleAt загружает модель с указанным окном и снимает занятую память.
func sampleAt(ctx context.Context, c *client, model string, window int,
	loadWait time.Duration, keepAlive string, baselineMiB float64) (vram.Sample, error) {

	loadCtx, cancel := context.WithTimeout(ctx, loadWait)
	defer cancel()

	if err := c.load(loadCtx, model, window, keepAlive); err != nil {
		return vram.Sample{}, err
	}
	r, err := c.waitLoaded(loadCtx, model, window, loadWait)
	if err != nil {
		return vram.Sample{}, err
	}
	// Расход берём по карте: /api/ps его занижает, и тем сильнее, чем больше
	// окно (см. комментарий к vram.Sample). Фон — чужие процессы — вычитаем.
	gpu := readGPU(loadCtx)
	return vram.Sample{
		Context:    r.ContextLength,
		UsageMiB:   gpu.UsedMiB - baselineMiB,
		GPUUsedMiB: gpu.UsedMiB,
		PSTotalMiB: float64(r.Size) / (1 << 20),
		PSVRAMMiB:  float64(r.SizeVRAM) / (1 << 20),
	}, nil
}

// samplePoints выбирает окна для замеров.
//
// По умолчанию берём три точки в верхней половине допустимого диапазона:
// зависимость памяти от окна не идеально линейна, и мерить надо там, где
// предстоит работать, а не на игрушечных окнах.
func samplePoints(arg string, maxContext int) ([]int, error) {
	if strings.TrimSpace(arg) != "" {
		pts, err := parseInts(arg)
		if err != nil {
			return nil, fmt.Errorf("-points: %w", err)
		}
		return clampPoints(pts, maxContext), nil
	}
	if maxContext <= 0 {
		maxContext = 32768
	}
	pts := []int{maxContext / 8, maxContext / 4, maxContext / 2}
	return clampPoints(pts, maxContext), nil
}

func clampPoints(pts []int, maxContext int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(pts))
	for _, p := range pts {
		p = p / 1024 * 1024
		if p < 1024 {
			p = 1024
		}
		if maxContext > 0 && p > maxContext {
			p = maxContext
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// budgetFor определяет, сколько видеопамяти можно отдать модели.
//
// Ёмкость карты — не тот бюджет, который доступен модели: часть съедают
// контекст CUDA и соседние процессы. Эту разницу видно как расхождение между
// nvidia-smi и суммой из /api/ps, поэтому вычитаем её, а не гадаем.
func budgetFor(gpu vram.GPU, baseline, override, reserve float64) vram.Budget {
	b := vram.Budget{ReserveMiB: reserve}
	if override > 0 {
		b.UsableMiB = override - reserve
		b.Source = "задан флагом -budget-mib"
		return b
	}
	if !gpu.Available {
		b.Source = "nvidia-smi недоступна — бюджет неизвестен, задайте -budget-mib"
		return b
	}
	b.TotalMiB = gpu.TotalMiB
	// Из ёмкости карты вычитаем фон — то, что заняли чужие процессы до начала
	// замеров. Расход самой модели уже учтён в замерах целиком: он снимается
	// по карте, а не по /api/ps.
	b.OverheadMiB = baseline
	b.UsableMiB = gpu.TotalMiB - b.OverheadMiB - reserve
	b.Source = "nvidia-smi за вычетом накладных расходов и резерва"
	return b
}

// findCeiling ищет наибольшее окно, при котором вытеснения ещё нет.
//
// Просто поверить расчёту нельзя: на qwen3.5:122b зависимость памяти от окна
// линейна до 128k, а выше идёт заметно круче, и предсказанные 175k уже
// вытесняются. Поэтому потолок ищется делением пополам между заведомо
// работающим замером и предсказанием, и каждая проба — настоящая загрузка.
func findCeiling(ctx context.Context, c *client, res *vram.ModelResult, probes int,
	loadWait time.Duration, keepAlive string, baselineMiB float64) {

	predicted := 0
	for _, u := range res.Users {
		if u.Users == 1 {
			predicted = u.ContextMax
		}
	}
	if predicted <= 0 || probes <= 0 {
		return
	}

	good := res.Fit.MaxMeasuredContext() // это окно уже проверено замером
	hi := predicted
	if hi <= good {
		res.VerifiedMaxContext = good
		fmt.Printf("потолок не выше проверенных %s — принимаем его\n", formatTokens(good))
		return
	}

	fmt.Printf("\nищу потолок загрузкой, до %d проб (известно рабочее %s, расчёт даёт %s)\n",
		probes, formatTokens(good), formatTokens(hi))

	for i := 0; i < probes && hi-good > 1024; i++ {
		try := hi
		if i > 0 || try == good {
			try = (good + hi) / 2 / 1024 * 1024
		}
		if try <= good || try >= hi+1 && i > 0 && try == hi {
			break
		}
		s, err := sampleAt(ctx, c, res.Name, try, loadWait, keepAlive, baselineMiB)
		if err != nil {
			fmt.Printf("  %-8s не удалось: %v\n", formatTokens(try), err)
			break
		}
		if s.Spilled() {
			fmt.Printf("  %-8s вытеснение (Ollama сообщает %.0f МиБ, в видеопамяти %.0f)\n",
				formatTokens(try), s.PSTotalMiB, s.PSVRAMMiB)
			hi = try
			continue
		}
		fmt.Printf("  %-8s целиком на GPU (%.0f МиБ на карте)\n", formatTokens(try), s.UsageMiB)
		good = try
		res.Fit.Samples = append(res.Fit.Samples, s)
	}

	res.VerifiedMaxContext = good
	fmt.Printf("подтверждённый потолок для одного пользователя: %s\n", formatTokens(good))

	// Пересобираем таблицу с учётом проверенного потолка. Делить его на число
	// пользователей вслепую нельзя: он мог упереться в максимум модели, а не
	// в память — тогда памяти хватит всем и делить нечего.
	res.VerifiedMaxContext = good
	for i := range res.Users {
		res.Users[i].ContextMax = res.Fit.ContextForUsers(
			res.Budget.UsableMiB, res.Users[i].Users, res.MaxContext, good)
	}
	res.FullContextUsers = res.Fit.UsersAtFullContext(res.Budget.UsableMiB, res.MaxContext, good)
}

// ── Вывод ────────────────────────────────────────────────────────────────────

func printHeader(url string, gpu vram.GPU, parallel int) {
	fmt.Println("olldiag — замер видеопамяти под контекст Ollama")
	fmt.Println("сервер:", url)
	if gpu.Available {
		fmt.Printf("карта:  %s, %.0f МиБ всего, %.0f занято сейчас\n",
			gpu.Name, gpu.TotalMiB, gpu.UsedMiB)
	} else {
		fmt.Println("карта:  nvidia-smi недоступна — запустите инструмент на самом сервере")
	}
	fmt.Printf("слотов на модель (OLLAMA_NUM_PARALLEL): %d\n", parallel)
	if parallel > 1 {
		fmt.Println("  внимание: при нескольких слотах Ollama резервирует кэш на каждый,")
		fmt.Println("  поэтому замеры уже включают это умножение")
	}
}

func printModelList(all []modelInfo) {
	fmt.Print("\nМодели на сервере (укажите нужные флагом -models):\n\n")
	tb := newTable(
		column{"модель", alignLeft},
		column{"размер файла", alignRight},
		column{"квантование", alignLeft},
	)
	for _, m := range all {
		tb.row(m.Name,
			fmt.Sprintf("%.2f ГиБ", float64(m.Size)/(1<<30)),
			m.Details.QuantizationLevel)
	}
	fmt.Println(tb)
	fmt.Println()
	fmt.Println("Пример: olldiag -models gemma3:4b -verify")
	fmt.Println("Осторожно: каждая точка замера — это полная перезагрузка модели.")
}

func printModelReport(res vram.ModelResult, withTable bool) {
	f := res.Fit
	fmt.Println()
	fmt.Printf("вес модели и постоянные расходы (A): %.0f МиБ (%.2f ГиБ)\n", f.BaseMiB, f.BaseMiB/1024)
	fmt.Printf("расход на токен окна (k):            %.0f байт (%.1f КиБ)\n",
		f.Slope(), f.Slope()/1024)
	if !f.Linear() {
		fmt.Printf("  наклон непостоянен: по всем точкам %.1f КиБ, по верхним двум %.1f КиБ —\n",
			f.BytesPerToken/1024, f.TopBytesPerToken/1024)
		fmt.Printf("  расчёт ведётся по большему, иначе потолок вышел бы завышенным\n")
	}
	fmt.Printf("наибольшее отклонение от прямой:     %.0f МиБ\n", f.MaxDeviationMiB)
	if res.Budget.UsableMiB > 0 {
		fmt.Printf("бюджет видеопамяти:                  %.0f МиБ (%s)\n",
			res.Budget.UsableMiB, res.Budget.Source)
		if res.Budget.OverheadMiB > 0 {
			fmt.Printf("  из них вне учёта Ollama:           %.0f МиБ\n", res.Budget.OverheadMiB)
			warnIfOverheadHigh(res.Budget)
		}
	} else {
		fmt.Println("бюджет видеопамяти неизвестен —", res.Budget.Source)
		return
	}

	if withTable {
		printUsersTable(res)
	}
}

// printUsersTable печатает, какое окно достанется каждому из N пользователей.
func printUsersTable(res vram.ModelResult) {
	fmt.Println()
	source := "расчёт"
	if res.VerifiedMaxContext > 0 {
		source = "проверено загрузкой"
	} else if measured := res.Fit.MaxMeasuredContext(); measured > 0 {
		for _, u := range res.Users {
			if u.Users == 1 && u.ContextMax > measured {
				source = "ЭКСТРАПОЛЯЦИЯ за пределы замеров — проверьте флагом -verify"
			}
		}
	}
	tb := newTable(
		column{"пользователей", alignRight},
		column{"окно на каждого", alignRight},
		column{"прогноз занятой памяти", alignRight},
		column{"примечание", alignLeft},
	)
	for _, u := range res.Users {
		note := ""
		if res.MaxContext > 0 && u.ContextMax >= res.MaxContext {
			note = "упирается в максимум модели"
		}
		// Если прогноз подбирается к бюджету вплотную, об этом надо сказать:
		// выше измеренного диапазона расход растёт быстрее прямой, и запас
		// в несколько процентов может съесться целиком.
		//
		// Но не на строке для одного пользователя с проверенным потолком:
		// она снята замером, и советовать «проверьте загрузкой» там нелепо.
		measured := u.Users == 1 && res.VerifiedMaxContext > 0
		if b := res.Budget.UsableMiB; b > 0 && !measured &&
			res.Fit.Predict(u.ContextMax, u.Users) > b*0.9 {
			if note != "" {
				note += "; "
			}
			note += "ВПРИТЫК — проверьте загрузкой"
		}
		if u.ContextMax == 0 {
			tb.row(strconv.Itoa(u.Users), "—", "—", "не помещается даже с минимальным окном")
			continue
		}
		tb.row(strconv.Itoa(u.Users), formatTokens(u.ContextMax),
			fmt.Sprintf("%.0f МиБ", res.Fit.Predict(u.ContextMax, u.Users)), note)
	}
	fmt.Println(tb)
	fmt.Println()
	fmt.Println("источник значений:", source)
	printFullContextVerdict(res)
}

// printFullContextVerdict отвечает на вопрос «сколько нас поместится, если
// каждому дать полное окно модели».
func printFullContextVerdict(res vram.ModelResult) {
	if res.MaxContext <= 0 || res.Budget.UsableMiB <= 0 {
		return
	}
	fmt.Println()
	if res.FullContextUsers > 0 {
		// Имя модели прямо в строке: вывод длинный, и бежать глазами вверх
		// за тем, о какой модели речь, не должно приходиться.
		fmt.Printf("Полное окно модели %s (%s) одновременно потянут: %d %s\n",
			res.Name, formatTokens(res.MaxContext), res.FullContextUsers,
			pluralUsers(res.FullContextUsers))
		fmt.Printf("  всего это займёт %.0f МиБ из %.0f доступных\n",
			res.Fit.Predict(res.MaxContext, res.FullContextUsers), res.Budget.UsableMiB)
		return
	}
	fmt.Printf("Полное окно модели %s (%s) не потянет ни один пользователь.\n",
		res.Name, formatTokens(res.MaxContext))
	single := res.Fit.ContextForUsers(res.Budget.UsableMiB, 1, res.MaxContext, res.VerifiedMaxContext)
	if single > 0 {
		fmt.Printf("  Максимум для одного пользователя: %s (%d токенов).\n",
			formatTokens(single), single)
	} else {
		fmt.Printf("  Модель не помещается в видеопамять даже с минимальным окном.\n")
	}
}

// pluralUsers склоняет слово «пользователь» — иначе строка режет глаз.
func pluralUsers(n int) string {
	mod100 := n % 100
	if mod100 >= 11 && mod100 <= 14 {
		return "пользователей"
	}
	switch n % 10 {
	case 1:
		return "пользователь"
	case 2, 3, 4:
		return "пользователя"
	default:
		return "пользователей"
	}
}

// warnIfOverheadHigh предупреждает о заниженном бюджете.
//
// Контекст CUDA стоит порядка полутора-двух гигабайт. Если насчиталось заметно
// больше, значит во время замера картой пользовался кто-то ещё: его память
// попала в «накладные расходы», бюджет вышел заниженным, а с ним занижены и
// все выводы. Молчать об этом нельзя — числа выглядят достоверно.
func warnIfOverheadHigh(b vram.Budget) {
	if b.OverheadMiB <= 4096 {
		return
	}
	fmt.Printf("  ВНИМАНИЕ: вне учёта Ollama насчитано %.0f МиБ — это много.\n", b.OverheadMiB)
	fmt.Println("  Обычно контекст CUDA занимает 1.5–2.5 ГиБ, а лишнее означает, что")
	fmt.Println("  во время замера на карте работал кто-то ещё. Бюджет вышел заниженным,")
	fmt.Println("  а с ним и число пользователей. Повторите замер на свободной карте.")
}

// formatTokens печатает окно в привычном виде: 131072 → 128k.
func formatTokens(n int) string {
	if n <= 0 {
		return "—"
	}
	if n%1024 == 0 {
		return fmt.Sprintf("%dk", n/1024)
	}
	return strconv.Itoa(n)
}

// ── Профиль ──────────────────────────────────────────────────────────────────

// ── Мелочи ───────────────────────────────────────────────────────────────────

func pickModels(all []modelInfo, arg string) ([]modelInfo, error) {
	if strings.EqualFold(strings.TrimSpace(arg), "all") {
		return all, nil
	}
	byName := make(map[string]modelInfo, len(all))
	for _, m := range all {
		byName[m.Name] = m
	}
	var out []modelInfo
	for _, name := range strings.Split(arg, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		m, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("модель %q не найдена на сервере", name)
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("не выбрано ни одной модели")
	}
	return out, nil
}

func parseInts(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		mult := 1
		if l := strings.ToLower(part); strings.HasSuffix(l, "k") {
			mult, part = 1024, part[:len(part)-1]
		}
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("не число: %q", part)
		}
		if n <= 0 {
			return nil, fmt.Errorf("должно быть положительным: %d", n)
		}
		out = append(out, n*mult)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("пустой список")
	}
	return out, nil
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// userRange превращает 8 в "1,2,3,4,5,6,7,8": пользователю нужен весь ряд,
// а не только степени двойки — по нему видно, где проходит граница.
func userRange(n int) string {
	if n < 1 {
		n = 1
	}
	parts := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		parts = append(parts, strconv.Itoa(i))
	}
	return strings.Join(parts, ",")
}

// printSummary сводит все измеренные модели в одну таблицу: по ней видно,
// какую модель выбрать под нужное число людей и окно.
func printSummary(models []vram.ModelResult) {
	if len(models) < 2 {
		return
	}
	fmt.Print("\n══ Сводка: окно контекста на каждого пользователя ══\n\n")

	// Заголовок собираем по первой модели: ряд пользователей у всех один.
	cols := []column{
		{"модель", alignLeft},
		{"размер файла", alignRight},
		{"потолок", alignRight},
		{"полное", alignRight},
	}
	for _, u := range models[0].Users {
		cols = append(cols, column{strconv.Itoa(u.Users), alignRight})
	}
	tb := newTable(cols...)

	for _, m := range models {
		ceiling := "—"
		if m.VerifiedMaxContext > 0 {
			ceiling = formatTokens(m.VerifiedMaxContext)
		}
		full := "никому"
		if m.FullContextUsers > 0 {
			full = fmt.Sprintf("%d чел.", m.FullContextUsers)
		}
		size := "—"
		if m.FileSizeMiB > 0 {
			size = fmt.Sprintf("%.2f ГиБ", m.FileSizeMiB/1024)
		}
		cells := []string{m.Name, size, ceiling, full}
		for _, u := range m.Users {
			cells = append(cells, formatTokens(u.ContextMax))
		}
		tb.row(cells...)
	}
	fmt.Println(tb)

	fmt.Println()
	fmt.Println("потолок — наибольшее окно для одного пользователя, проверенное загрузкой")
	fmt.Println("полное  — сколько человек одновременно потянут полное окно модели")
	fmt.Println("столбцы — сколько достанется каждому при таком числе одновременных сессий")
}

// runRecalc пересобирает таблицы пользователей из сохранённого профиля.
//
// Замеры стоят десятков минут загрузок, а таблицы из них выводятся мгновенно.
// Поэтому исправление формулы или другой ряд пользователей не требуют
// повторного прогона: достаточно пересчитать уже собранные числа.
func runRecalc(path string, calc int, usersArg, outPath string) error {
	profile, err := vram.LoadProfile(path)
	if err != nil {
		return err
	}
	if profile.Outdated() {
		return fmt.Errorf("профиль снят старой версией инструмента (формат %d вместо %d):\n"+
			"  расход тогда считался по /api/ps и занижался в разы.\n"+
			"  Пересчитывать нечего — нужен новый замер: olldiag -calc N",
			profile.Format, vram.ProfileFormat)
	}
	if calc > 0 {
		usersArg = userRange(calc)
	}
	userCounts, err := parseInts(usersArg)
	if err != nil {
		return fmt.Errorf("-users: %w", err)
	}

	fmt.Printf("пересчёт из профиля %s (сервер %s, снят %s)\n",
		path, profile.Host, profile.GeneratedAt)

	for i := range profile.Models {
		m := &profile.Models[i]
		m.Users = m.Users[:0]
		for _, u := range userCounts {
			m.Users = append(m.Users, vram.UserLimit{
				Users: u,
				ContextMax: m.Fit.ContextForUsers(
					m.Budget.UsableMiB, u, m.MaxContext, m.VerifiedMaxContext),
			})
		}
		m.FullContextUsers = m.Fit.UsersAtFullContext(m.Budget.UsableMiB, m.MaxContext, m.VerifiedMaxContext)
		fmt.Printf("\n=== %s ===\n", m.Name)
		fmt.Printf("максимум окна %s, потолок для одного %s",
			formatTokens(m.MaxContext), formatTokens(m.VerifiedMaxContext))
		if vram.LimitedByModel(m.VerifiedMaxContext, m.MaxContext) {
			fmt.Print(" — это максимум самой модели, видеопамять не предел")
		}
		fmt.Println()
		printUsersTable(*m)
		warnIfOverheadHigh(m.Budget)
	}

	printSummary(profile.Models)
	if outPath == "" {
		outPath = path
	}
	if err := vram.WriteProfile(outPath, *profile); err != nil {
		return err
	}
	fmt.Printf("\nПрофиль обновлён: %s\n", outPath)
	return nil
}
