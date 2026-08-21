package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config — настройки прогонов: адрес сервера, расписание ночи, правила проверки
// занятости карты, параметры самого прогона и лимиты контейнеров.
//
// Всё, что решает «когда начинать, когда заканчивать и как понять, что карта
// свободна», живёт здесь, а не в коде и не в скриптах: эти числа правятся чаще
// всего, и держать их в трёх местах значит однажды развести их между собой.
// Флаги командной строки перекрывают конфиг, конфиг перекрывает умолчания.
type Config struct {
	URL      string    `toml:"url"`
	Root     string    `toml:"root"`
	Schedule Schedule  `toml:"schedule"`
	Guard    GuardCfg  `toml:"guard"`
	Run      RunCfg    `toml:"run"`
	Verify   VerifyCfg `toml:"verify"`
	Health   HealthCfg `toml:"health"`

	path string // откуда прочитан
}

// Schedule — когда стенд наш. Расписание задаётся промежутками на каждый день
// недели: у понедельника и субботы механизм один и тот же, разные только числа.
//
// Часовой пояс указывается явно. Стенд с 21.08.2026 живёт по Москве, но пояс
// сервера могут снова сменить, а окна назначены в московском времени.
type Schedule struct {
	Timezone string      `toml:"timezone"`
	Wait     Duration    `toml:"wait"`
	Gap      Duration    `toml:"gap"`
	Days     DaySchedule `toml:"days"`

	// Устаревший вид расписания: одно ночное окно на будни плюс отдельные
	// выходные. Читается ради старых файлов настроек и при загрузке
	// разворачивается в Days — дальше код знает только про дни недели.
	Start   string  `toml:"start"`
	Until   string  `toml:"until"`
	Restore string  `toml:"restore"`
	Weekend Weekend `toml:"weekend"`
}

// DaySchedule — промежутки на каждый день недели. Промежуток пишется как
// "ЧЧ:ММ-ЧЧ:ММ", их может быть несколько за день: например, утро наше, днём
// работают люди, вечер снова наш. Промежуток через полночь ("22:00-02:00")
// относится к тому дню, в котором начался. Пустой список — в этот день
// прогонов нет.
type DaySchedule struct {
	Monday    []string `toml:"monday"`
	Tuesday   []string `toml:"tuesday"`
	Wednesday []string `toml:"wednesday"`
	Thursday  []string `toml:"thursday"`
	Friday    []string `toml:"friday"`
	Saturday  []string `toml:"saturday"`
	Sunday    []string `toml:"sunday"`
}

// For возвращает промежутки указанного дня недели.
func (d DaySchedule) For(wd time.Weekday) []string {
	switch wd {
	case time.Monday:
		return d.Monday
	case time.Tuesday:
		return d.Tuesday
	case time.Wednesday:
		return d.Wednesday
	case time.Thursday:
		return d.Thursday
	case time.Friday:
		return d.Friday
	case time.Saturday:
		return d.Saturday
	case time.Sunday:
		return d.Sunday
	}
	return nil
}

// Empty сообщает, что расписание не задано ни на один день.
func (d DaySchedule) Empty() bool {
	for wd := time.Sunday; wd <= time.Saturday; wd++ {
		if len(d.For(wd)) > 0 {
			return false
		}
	}
	return true
}

// Weekend — устаревшее описание выходных, оставлено для старых файлов настроек.
type Weekend struct {
	Enabled  bool     `toml:"enabled"`
	Saturday []string `toml:"saturday"`
	Sunday   []string `toml:"sunday"`
}

// Normalize разворачивает устаревшее расписание в промежутки по дням.
// Вызывается при загрузке, поэтому весь остальной код знает только про Days.
func (s *Schedule) Normalize() error {
	if !s.Days.Empty() {
		return nil
	}
	if s.Start == "" || s.Restore == "" {
		return nil
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return fmt.Errorf("часовой пояс %q: %w", s.Timezone, err)
	}
	night := s.Start + "-" + s.Restore
	s.Days.Monday = []string{night}
	s.Days.Tuesday = []string{night}
	s.Days.Wednesday = []string{night}
	s.Days.Thursday = []string{night}
	s.Days.Friday = []string{night}
	s.Days.Saturday = []string{night}
	s.Days.Sunday = []string{night}
	if s.Weekend.Enabled {
		if len(s.Weekend.Saturday) > 0 {
			s.Days.Saturday = s.Weekend.Saturday
		}
		if len(s.Weekend.Sunday) > 0 {
			s.Days.Sunday = s.Weekend.Sunday
		}
	}
	// Запас между «перестать раздавать задачи» и «открыть сервер» раньше
	// задавался парой until/restore — переводим его в gap.
	if s.Gap == 0 && s.Until != "" {
		until, err1 := minutesOfDay(s.Until, loc)
		restore, err2 := minutesOfDay(s.Restore, loc)
		if err1 == nil && err2 == nil {
			diff := restore - until
			if diff < 0 {
				diff += 24 * 60
			}
			s.Gap = Duration(time.Duration(diff) * time.Minute)
		}
	}
	return nil
}

// HealthCfg — слежение за здоровьем прогона: выезд модели в оперативную память
// и зависание генерации. И то и другое обесценивает ночь, если не заметить.
type HealthCfg struct {
	CheckSpill         bool     `toml:"check_spill"`
	SpillAction        string   `toml:"spill_action"` // skip — пропустить модель, restart — перезапустить Ollama
	RestartAfterErrors int      `toml:"restart_after_errors"`
	RestartWait        Duration `toml:"restart_wait"`
}

// GuardCfg — правило номер один: когда считать карту свободной.
type GuardCfg struct {
	FreeChecks    int      `toml:"free_checks"`
	Poll          Duration `toml:"poll"`
	JournalWindow Duration `toml:"journal_window"`
	CheckGPU      bool     `toml:"check_gpu"`
	CheckPS       bool     `toml:"check_ps"`
	CheckJournal  bool     `toml:"check_journal"`
	CheckSessions bool     `toml:"check_sessions"`

	// CheckService — требовать, чтобы служба Ollama была запущена.
	// Остановленная служба — это не «карта свободна», а наоборот: её обычно
	// гасят руками, чтобы освободить видеопамять под обучение. Начать прогон
	// в такой момент значит поднять службу за спиной у человека и отобрать
	// у него карту посреди работы.
	CheckService bool   `toml:"check_service"`
	Service      string `toml:"service"`

	// BusyVRAMMiB и BusyUtilPercent — занятость самой карты, без оглядки
	// на то, чей это процесс. Список процессов не всегда виден (контейнеры,
	// чужие права, MIG), а занятая память и загрузка видны всегда.
	BusyVRAMMiB     int `toml:"busy_vram_mib"`
	BusyUtilPercent int `toml:"busy_util_percent"`

	// IdleBeforeStart — сколько карта должна быть **непрерывно** свободна,
	// прежде чем мы сами поднимем погашенную службу Ollama.
	//
	// Службу гасят под обучение, а включить обратно забывают. Ждать до утра
	// в такой ситуации жалко: карта простаивает. Но и поднимать её сразу
	// нельзя — между эпохами обучения бывают паузы, и «свободно» в этот момент
	// означает лишь «сейчас не считает». Отсюда выдержка: считается только
	// **наблюдённый** простой, счёт сбрасывается при любой занятости и при
	// перерыве в наблюдении. 0 — никогда не поднимать службу самим.
	IdleBeforeStart Duration `toml:"idle_before_start"`

	// LogChecks — писать ли каждую проверку в logs/guard.log. По нему потом
	// видно, почему прогон не начался или, наоборот, начался.
	LogChecks bool `toml:"log_checks"`
}

// RunCfg — параметры самого прогона.
type RunCfg struct {
	Suites    string   `toml:"suites"`
	Repeats   int      `toml:"repeats"`
	NumCtx    int      `toml:"num_ctx"`
	Retries   int      `toml:"retries"`
	Timeout   Duration `toml:"timeout"`
	KeepAlive string   `toml:"keep_alive"`
	Exclude   []string `toml:"exclude"`
}

// VerifyCfg — лимиты контейнера проверки.
type VerifyCfg struct {
	Memory  string   `toml:"memory"`
	CPUs    string   `toml:"cpus"`
	Timeout Duration `toml:"timeout"`
}

// DefaultConfig — умолчания, отвечающие договорённости с владельцем стенда:
// окно 00:00–07:00 по Москве, старт после трёх свободных проверок подряд.
func DefaultConfig() Config {
	return Config{
		URL:  "http://127.0.0.1:11434",
		Root: defaultRoot(),
		Schedule: Schedule{
			Timezone: "Europe/Moscow",
			Wait:     Duration(6 * time.Hour),
			Gap:      Duration(15 * time.Minute),
			Days: DaySchedule{
				Monday:    []string{"00:00-07:00"},
				Tuesday:   []string{"00:00-07:00"},
				Wednesday: []string{"00:00-07:00"},
				Thursday:  []string{"00:00-07:00"},
				Friday:    []string{"00:00-07:00"},
				Saturday:  []string{"00:00-23:59"},
				Sunday:    []string{"00:00-23:59"},
			},
		},
		Guard: GuardCfg{
			FreeChecks: 3, Poll: Duration(time.Minute), JournalWindow: Duration(15 * time.Minute),
			CheckGPU: true, CheckPS: true, CheckJournal: true, CheckSessions: true,
			CheckService: true, Service: "ollama",
			BusyVRAMMiB: 1024, BusyUtilPercent: 20,
			IdleBeforeStart: Duration(20 * time.Minute), LogChecks: true,
		},
		Run: RunCfg{
			Suites: "all", Repeats: 3, NumCtx: 32768, Retries: 2,
			Timeout: Duration(20 * time.Minute), KeepAlive: "1h",
			Exclude: []string{"qwen38-szkm:latest"},
		},
		Verify: VerifyCfg{Memory: "2g", CPUs: "4", Timeout: Duration(10 * time.Minute)},
		Health: HealthCfg{
			CheckSpill: true, SpillAction: "skip",
			RestartAfterErrors: 2, RestartWait: Duration(5 * time.Minute),
		},
	}
}

// ConfigPath — где искать конфиг по умолчанию.
func ConfigPath(root string) string { return filepath.Join(root, "olleval.toml") }

// LoadConfig читает конфиг поверх умолчаний. Отсутствие файла — не ошибка:
// без него работают умолчания, и это нормальный способ запуска.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		path = ConfigPath(cfg.Root)
	}
	cfg.path = path
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.Schedule.Normalize(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Validate ловит то, из-за чего ночь пропала бы молча: неизвестный часовой пояс,
// время не того вида, бессмысленные числа.
func (c Config) Validate() error {
	loc, err := time.LoadLocation(c.Schedule.Timezone)
	if err != nil {
		return fmt.Errorf("часовой пояс %q: %w", c.Schedule.Timezone, err)
	}
	for name, v := range map[string]string{
		"schedule.start": c.Schedule.Start, "schedule.until": c.Schedule.Until,
		"schedule.restore": c.Schedule.Restore,
	} {
		if v == "" {
			continue // устаревшие поля: их может не быть вовсе
		}
		if _, err := time.ParseInLocation("15:04", v, loc); err != nil {
			return fmt.Errorf("%s = %q: нужен вид ЧЧ:ММ", name, v)
		}
	}
	for wd := time.Sunday; wd <= time.Saturday; wd++ {
		for _, span := range c.Schedule.Days.For(wd) {
			if _, _, err := parseSpan(span, loc); err != nil {
				return fmt.Errorf("schedule.days.%s: %w", strings.ToLower(wd.String()), err)
			}
		}
	}
	if a := c.Health.SpillAction; a != "" && a != "skip" && a != "restart" {
		return fmt.Errorf("health.spill_action = %q: допустимо skip или restart", a)
	}
	switch {
	case c.Guard.FreeChecks < 1:
		return fmt.Errorf("guard.free_checks = %d: нужно хотя бы одну проверку", c.Guard.FreeChecks)
	case c.Guard.Poll.Get(0) <= 0:
		return fmt.Errorf("guard.poll: нужен положительный промежуток")
	case c.Run.Repeats < 1:
		return fmt.Errorf("run.repeats = %d: нужен хотя бы один повтор", c.Run.Repeats)
	case c.Run.NumCtx < 1024:
		return fmt.Errorf("run.num_ctx = %d: слишком маленькое окно", c.Run.NumCtx)
	}
	return nil
}

// Get возвращает значение по имени вида "schedule.until" — этим пользуются
// скрипты ночи, чтобы не держать те же числа у себя второй раз.
func (c Config) Get(key string) (string, error) {
	switch strings.ToLower(key) {
	case "url":
		return c.URL, nil
	case "root":
		return c.Root, nil
	case "schedule.timezone":
		return c.Schedule.Timezone, nil
	case "schedule.wait":
		return time.Duration(c.Schedule.Wait).String(), nil
	case "guard.free_checks":
		return fmt.Sprint(c.Guard.FreeChecks), nil
	case "guard.poll":
		return time.Duration(c.Guard.Poll).String(), nil
	case "guard.journal_window":
		return time.Duration(c.Guard.JournalWindow).String(), nil
	case "guard.busy_vram_mib":
		return fmt.Sprint(c.Guard.BusyVRAMMiB), nil
	case "guard.busy_util_percent":
		return fmt.Sprint(c.Guard.BusyUtilPercent), nil
	case "guard.idle_before_start":
		return time.Duration(c.Guard.IdleBeforeStart).String(), nil
	case "run.suites":
		return c.Run.Suites, nil
	case "run.repeats":
		return fmt.Sprint(c.Run.Repeats), nil
	case "run.num_ctx":
		return fmt.Sprint(c.Run.NumCtx), nil
	case "run.exclude":
		return strings.Join(c.Run.Exclude, ","), nil
	case "schedule.gap":
		return time.Duration(c.Schedule.Gap).String(), nil
	case "schedule.days.monday", "schedule.days.tuesday", "schedule.days.wednesday",
		"schedule.days.thursday", "schedule.days.friday", "schedule.days.saturday",
		"schedule.days.sunday":
		day := strings.TrimPrefix(strings.ToLower(key), "schedule.days.")
		for wd := time.Sunday; wd <= time.Saturday; wd++ {
			if strings.ToLower(wd.String()) == day {
				return strings.Join(c.Schedule.Days.For(wd), ","), nil
			}
		}
		return "", fmt.Errorf("неизвестный день %q", day)
	case "health.restart_after_errors":
		return fmt.Sprint(c.Health.RestartAfterErrors), nil
	default:
		return "", fmt.Errorf("неизвестный ключ %q", key)
	}
}

// configTemplate — образец конфига с пояснениями. Пишется командой
// `olleval config --init`; готовый файл никогда не затирается.
const configTemplate = `# Настройки ночных прогонов olleval.
# Флаги командной строки перекрывают этот файл, файл перекрывает умолчания.

url  = "http://127.0.0.1:11434"   # на время прогона Ollama слушает только localhost
root = "%s"                        # корень данных: suites, fixtures, runs, state

# ── Когда стенд наш ──────────────────────────────────────────────────────────
[schedule]
timezone = "Europe/Moscow"    # пояс задаётся явно: пояс сервера могут сменить
wait     = "6h"               # сколько ждать освобождения карты, прежде чем сдаться
gap      = "15m"              # за сколько до конца окна перестать раздавать задачи:
                              # остаток нужен на выгрузку моделей и сводку

# Расписание на каждый день недели. Промежуток пишется как "ЧЧ:ММ-ЧЧ:ММ",
# их может быть несколько за день: например, ночь наша, днём работают люди,
# вечер снова наш. Промежуток через полночь ("22:00-02:00") относится к тому
# дню, в котором начался. Пустой список — в этот день прогонов нет.
#
#   monday = ["00:00-07:00", "13:00-14:00"]   — ночь и обеденный перерыв
#   friday = []                                — по пятницам не гоняем
[schedule.days]
monday    = ["00:00-07:00"]
tuesday   = ["00:00-07:00"]
wednesday = ["00:00-07:00"]
thursday  = ["00:00-07:00"]
friday    = ["00:00-07:00"]
saturday  = ["00:00-23:59"]
sunday    = ["00:00-23:59"]

# ── Здоровье прогона ─────────────────────────────────────────────────────────
[health]
check_spill          = true    # ловить выезд модели в оперативную память (size != size_vram)
spill_action         = "skip"  # skip — пропустить модель, restart — перезапустить Ollama
restart_after_errors = 2       # столько сорванных попыток подряд — перезапуск Ollama
restart_wait         = "5m"    # сколько ждать, пока Ollama поднимется после перезапуска

# ── Правило номер один: карта должна быть свободна ───────────────────────────
[guard]
free_checks    = 3      # столько свободных проверок подряд нужно для старта
poll           = "1m"   # как часто проверять; занятость сбрасывает счёт
journal_window = "15m"  # за какое время смотреть свежие запросы в журнале Ollama
check_gpu      = true   # процессы на видеокарте (nvidia-smi)
check_ps       = true   # загруженные модели (/api/ps)
check_journal  = true   # свежие POST /api/ в журнале службы
check_sessions = true   # чужие сеансы ssh — только к сведению, ночь не запрещают

# Остановленная служба Ollama — не «карта свободна», а наоборот: её гасят
# руками, чтобы освободить видеопамять под обучение модели питоновскими
# скриптами. Прогон в такой момент поднял бы службу за спиной у человека.
check_service    = true
service          = "ollama"

# Занятость самой карты, чей бы процесс её ни держал. Список процессов виден
# не всегда (контейнеры, чужие права, MIG), а память и загрузка — всегда.
busy_vram_mib     = 1024   # столько занятой видеопамяти уже считается «занято»
busy_util_percent = 20     # и столько процентов загрузки тоже

# Службу гасят под обучение, а включить обратно забывают — и карта простаивает
# до утра. Если она свободна непрерывно столько времени, прогон поднимает службу
# сам. Считается только наблюдённый простой: любая занятость и любой перерыв
# в наблюдении сбрасывают счёт. 0 — службу самим не поднимать.
idle_before_start = "20m"

# Каждая проверка пишется строкой в logs/guard.log — по нему видно, почему
# прогон не начался или начался.
log_checks = true

# ── Сам прогон ───────────────────────────────────────────────────────────────
[run]
suites     = "all"      # какие наборы гнать: all или список через запятую
repeats    = 3          # повторов каждой задачи: модель не функция, разброс нужен
num_ctx    = 32768      # одинаковое окно для всех: иначе сравнение нечестное
retries    = 2          # повторов обмена, сорванного до первого куска ответа
timeout    = "20m"      # предел на одну генерацию
keep_alive = "1h"       # сколько держать модель в памяти внутри её блока
exclude    = ["qwen38-szkm:latest"]   # кто не участвует

# ── Проверка ответов в контейнере ────────────────────────────────────────────
[verify]
memory  = "2g"
cpus    = "4"
timeout = "10m"
`

// WriteConfigTemplate создаёт образец конфига, не трогая существующий файл.
func WriteConfigTemplate(path, root string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("файл %s уже есть — не трогаю", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprintf(configTemplate, root)), 0o644)
}

// WatchSchedule возвращает функцию, которая перечитывает файл настроек и
// сообщает действующий предел прогона.
//
// Настройки читаются при запуске каждой команды, поэтому правку расписания
// подхватывает ближайший тик — не позже чем через четверть часа. Но **уже
// идущий** прогон так ничего бы не заметил: он держит свой предел в памяти
// сутками, если окно суточное. А правят расписание обычно ровно затем, чтобы
// забрать стенд себе прямо сейчас. Поэтому прогон перечитывает файл перед
// каждой задачей: окно убрали — останавливается, продлили — работает дальше.
//
// Перечитывается только расписание. Число повторов, окно контекста и прочее
// остаются такими, какими прогон начинался: смена на середине сделала бы
// половину ночи несравнимой с другой половиной.
func WatchSchedule(path string) func() (time.Time, bool) {
	return func() (time.Time, bool) {
		cfg, err := LoadConfig(path)
		if err != nil {
			return time.Time{}, true // файл сломали — не наше дело останавливать ночь
		}
		w, in, err := CurrentWindow(time.Now(), cfg.Schedule)
		if err != nil || !in {
			return time.Time{}, false
		}
		return w.Deadline, true
	}
}
