package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Guard — правило номер один: прогон не начинается, пока видеокарта занята.
// На стенде работают люди, и ночной замер не имеет права ни отобрать у них
// память, ни перезапустить службу под чужим запросом.
type Guard struct {
	Client *ollama.Client
	// Root — корень данных прогонов: там лежат state/gpu_idle.json
	// (наблюдённый простой карты) и logs/guard.log.
	Root string
	// Cfg — какие проверки делать и за какое время смотреть журнал.
	// Живёт в конфиге, а не в коде: это правится чаще всего остального.
	Cfg GuardCfg
	// Run — запуск внешней команды; подменяется в тестах.
	Run func(ctx context.Context, name string, args ...string) (string, int, error)

	// Limit — какая загрузка карты в этот момент ещё считается простоем.
	// Порог живёт в расписании: у ночи и у выходного дня он разный.
	// Пусто — общий guard.busy_util_percent.
	Limit func(now time.Time) int
}

// GuardReport — что показала проверка.
type GuardReport struct {
	Free     bool     `json:"free"`
	Blocking []string `json:"blocking,omitempty"` // из-за чего нельзя начинать
	Notes    []string `json:"notes,omitempty"`    // к сведению: чужие сеансы и прочее

	GPUUsedMiB   int           `json:"gpu_used_mib"`
	GPUUtil      int           `json:"gpu_util"` // наибольшая выборка серии
	GPUSamples   []int         `json:"gpu_samples,omitempty"`
	UtilLimit    int           `json:"util_limit"` // порог, действовавший при проверке
	ServiceState string        `json:"service_state,omitempty"`
	IdleFor      time.Duration `json:"idle_for"`

	// NeedServiceStart — карта свободна достаточно долго, а служба погашена:
	// её забыли поднять после обучения, и это можно сделать за них.
	NeedServiceStart bool `json:"need_service_start"`
}

// NewGuard готовит проверку по настройкам конфига.
func NewGuard(c *ollama.Client, cfg GuardCfg, root string) *Guard {
	return &Guard{Client: c, Cfg: cfg, Root: root, Run: runCommand}
}

// Check выполняет все проверки разом и говорит, свободна ли карта.
//
// Решает **загрузка** видеокарты, а не занятая на ней память. Модель остаётся
// в видеопамяти часами после последнего чужого запроса — по занятости памяти
// стенд выглядел бы вечно занятым, и ночь не начиналась бы вовсе. Порог
// загрузки берётся из промежутка расписания ("00:00-07:00 gpu<=10"), потому
// что ночью терпимость к чужой работе одна, а днём другая.
//
// Блокируют старт: загрузка выше порога и свежие запросы к Ollama. Забытая
// в памяти модель и процессы на карте при простое — только заметка: модель
// перед прогоном выгружается. Чужие сеансы ssh тоже лишь отмечаются: открытая
// сессия с незакрытым mc — это не работа, и запрещать по ней ночь значит
// не запускать её никогда.
//
// Если загрузку измерить не удалось (нет nvidia-smi, непонятный ответ),
// работает прежнее строгое правило: любой процесс на карте, занятая память
// и загруженная модель запрещают прогон. Слепой старт хуже пропущенной ночи.
func (g *Guard) Check(ctx context.Context) GuardReport {
	var rep GuardReport
	rep.UtilLimit = g.utilLimit()

	var procs []string
	if g.Cfg.CheckGPU {
		if out, _, err := g.Run(ctx, "nvidia-smi",
			"--query-compute-apps=pid,used_memory", "--format=csv,noheader"); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					procs = append(procs, line)
				}
			}
		} else {
			rep.Notes = append(rep.Notes, "nvidia-smi недоступен: "+err.Error())
		}
	}

	// Загрузка карты серией выборок: обучение модели питоновским скриптом
	// видно именно здесь, а вовсе не через Ollama — её на время такого
	// обучения обычно вообще останавливают.
	utilKnown := false
	if g.Cfg.CheckGPU {
		used, util, samples, ok := g.sampleGPU(ctx)
		if ok {
			utilKnown = true
			rep.GPUUsedMiB, rep.GPUUtil, rep.GPUSamples = used, util, samples
			if util > rep.UtilLimit {
				rep.Blocking = append(rep.Blocking,
					fmt.Sprintf("карта загружена на %d%% (порог %d%%)", util, rep.UtilLimit))
			}
		} else {
			rep.Notes = append(rep.Notes, "загрузку карты измерить не удалось — правила строгие")
		}
	}

	switch {
	case !utilKnown:
		for _, p := range procs {
			rep.Blocking = append(rep.Blocking, "на видеокарте работает процесс: "+p)
		}
		if g.Cfg.BusyVRAMMiB > 0 && rep.GPUUsedMiB >= g.Cfg.BusyVRAMMiB {
			rep.Blocking = append(rep.Blocking,
				fmt.Sprintf("на карте занято %d МиБ видеопамяти (порог %d)", rep.GPUUsedMiB, g.Cfg.BusyVRAMMiB))
		}
	default:
		for _, p := range procs {
			rep.Notes = append(rep.Notes, "на карте держит память процесс: "+p)
		}
	}

	// Состояние службы нужно раньше вердикта: по нему решается, стоит ли
	// жаловаться на молчащий /api/ps.
	serviceName := g.Cfg.Service
	if serviceName == "" {
		serviceName = "ollama"
	}
	if g.Cfg.CheckService {
		if out, _, err := g.Run(ctx, "systemctl", "is-active", serviceName); err == nil {
			rep.ServiceState = strings.TrimSpace(out)
		} else {
			rep.Notes = append(rep.Notes, "не удалось спросить состояние службы "+serviceName+": "+err.Error())
		}
	}

	if g.Cfg.CheckPS {
		if running, err := g.Client.PS(ctx); err != nil {
			// Погашенная служба и так уже названа причиной — повторять,
			// что её API не отвечает, значит удлинять каждую строку журнала
			// одним и тем же.
			if rep.ServiceState == "" || rep.ServiceState == "active" {
				rep.Notes = append(rep.Notes, "не удалось спросить /api/ps: "+err.Error())
			}
		} else {
			for _, m := range running {
				msg := fmt.Sprintf("в память сервера загружена модель %s (%.1f ГиБ)",
					m.Name, float64(m.SizeVRAM)/(1<<30))
				if utilKnown && rep.GPUUtil <= rep.UtilLimit {
					// Забытая в памяти модель — не работа: карта при этом
					// не считает, а перед прогоном мы её выгрузим сами.
					rep.Notes = append(rep.Notes, msg+" — карта простаивает, выгружу перед прогоном")
				} else {
					rep.Blocking = append(rep.Blocking, msg)
				}
			}
		}
	}

	if window := time.Duration(g.Cfg.JournalWindow); g.Cfg.CheckJournal && window > 0 {
		since := "-" + strconv.Itoa(int(window.Minutes())) + " min"
		if out, _, err := g.Run(ctx, "sudo", "journalctl", "-u", "ollama", "--since", since, "--no-pager", "-q"); err == nil {
			if n := strings.Count(out, "POST /api/"); n > 0 {
				rep.Blocking = append(rep.Blocking,
					fmt.Sprintf("за последние %.0f мин к Ollama пришло %d запросов", window.Minutes(), n))
			}
		} else {
			rep.Notes = append(rep.Notes, "журнал Ollama недоступен: "+err.Error())
		}
	}

	// Занятость карты для счёта простоя и для решения о службе: заметки
	// (забытая модель, чужая память при нулевой загрузке) в неё не входят.
	gpuBusy := len(rep.Blocking) > 0

	// Наблюдённый простой карты. Считается только то, что мы видели своими
	// глазами: занятость и перерыв в наблюдении сбрасывают счёт. Иначе вчерашняя
	// отметка выдавала бы «свободна двадцать часов» там, где карту всё это время
	// не проверяли вовсе.
	rep.IdleFor = g.trackIdle(!gpuBusy)

	// Остановленная служба — обычно знак «карту забрали»: её гасят, чтобы
	// освободить видеопамять под обучение. Но её так же часто забывают поднять
	// обратно, и тогда карта простаивает зря. Поэтому смотрим на выдержку:
	// пока простой короткий — считаем стенд занятым, а как только он подтвердил
	// себя, службу можно поднять и начать прогон.
	if state := rep.ServiceState; g.Cfg.CheckService && state != "" && state != "active" {
		wait := time.Duration(g.Cfg.IdleBeforeStart)
		switch {
		case gpuBusy || wait <= 0:
			rep.Blocking = append(rep.Blocking, fmt.Sprintf(
				"служба %s не запущена (%s) — её гасят, когда карта нужна под другое", serviceName, state))
		case rep.IdleFor >= wait:
			rep.NeedServiceStart = true
			rep.Notes = append(rep.Notes, fmt.Sprintf(
				"служба %s погашена (%s), но карта свободна уже %s — поднимаю службу сам",
				serviceName, state, round(rep.IdleFor)))
		default:
			rep.Blocking = append(rep.Blocking, fmt.Sprintf(
				"служба %s не запущена (%s); карта свободна %s из нужных %s",
				serviceName, state, round(rep.IdleFor), wait))
		}
	}

	if out, _, err := g.Run(ctx, "w", "-h"); g.Cfg.CheckSessions && err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line == "" {
				continue
			}
			user := strings.Fields(line)[0]
			if user != currentUser() {
				rep.Notes = append(rep.Notes, "в системе чужой сеанс: "+strings.TrimSpace(line))
			}
		}
	}

	rep.Free = len(rep.Blocking) == 0
	g.logCheck(rep)
	return rep
}

// utilLimit — порог загрузки, действующий сейчас.
func (g *Guard) utilLimit() int {
	if g.Limit == nil {
		return g.Cfg.BusyUtilPercent
	}
	return g.Limit(time.Now())
}

// sampleGPU меряет загрузку карты серией выборок и возвращает наибольшую.
//
// Одна выборка nvidia-smi — мгновенный снимок. Между двумя токенами чужого
// ответа загрузка падает в ноль, и единственный замер сказал бы «свободно»
// посреди чужой работы. Занятая память берётся тоже наибольшая: она нужна
// только для журнала и для строгого правила при неудачном замере.
func (g *Guard) sampleGPU(ctx context.Context) (usedMiB, utilPercent int, samples []int, ok bool) {
	n := g.Cfg.UtilSamples
	if n < 1 {
		n = 1
	}
	gap := time.Duration(g.Cfg.UtilSampleGap)
	if gap <= 0 {
		gap = time.Second
	}
	for i := 0; i < n; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return usedMiB, utilPercent, samples, ok
			case <-time.After(gap):
			}
		}
		out, _, err := g.Run(ctx, "nvidia-smi",
			"--query-gpu=memory.used,utilization.gpu", "--format=csv,noheader,nounits")
		if err != nil {
			continue
		}
		used, util, good := parseGPUUsage(out)
		if !good {
			continue
		}
		ok = true
		samples = append(samples, util)
		if used > usedMiB {
			usedMiB = used
		}
		if util > utilPercent {
			utilPercent = util
		}
	}
	return usedMiB, utilPercent, samples, ok
}

// round округляет длительность до секунд — в журнале наносекунды не нужны.
func round(d time.Duration) time.Duration { return d.Round(time.Second) }

// WaitFree ждёт, пока карта окажется свободной **подряд** need раз с шагом poll.
//
// Одной проверки мало. Человек может сесть за стенд в полночь и проработать
// десять минут: единственная проверка в 00:00 увидела бы занятую карту и
// отменила всю ночь, а проверка «раз в четверть часа» рискует попасть в паузу
// между двумя его запросами и отобрать память посреди работы. Поэтому опрос
// частый, а старт — только после нескольких свободных проверок подряд: любая
// занятость сбрасывает счётчик и всё начинается сначала.
//
// giveUp — момент, после которого ждать бессмысленно (ночь всё равно не успеет).
// Возвращает последний отчёт и признак «дождались».
func (g *Guard) WaitFree(ctx context.Context, need int, poll time.Duration, giveUp time.Time, log func(string)) (GuardReport, bool) {
	if need < 1 {
		need = 1
	}
	var rep GuardReport
	var streak int
	for {
		rep = g.Check(ctx)
		if rep.Free {
			streak++
			if streak >= need {
				log(fmt.Sprintf("карта свободна %d проверок подряд — начинаю", streak))
				return rep, true
			}
			log(fmt.Sprintf("карта свободна: %d из %d проверок подряд", streak, need))
		} else {
			if streak > 0 {
				log("карта снова занята — счёт свободных проверок сброшен")
			}
			streak = 0
			for _, b := range rep.Blocking {
				log("занято: " + b)
			}
		}

		if !giveUp.IsZero() && time.Now().Add(poll).After(giveUp) {
			// Разовая проверка (ждать не просили) не должна писать «карта так
			// и не освободилась» о свободной карте: утром по такой строке
			// journal читается ровно наоборот.
			if rep.Free {
				log(fmt.Sprintf("карта свободна, но для старта нужно %d проверок подряд — ждать не просили", need))
			} else {
				log("время ожидания вышло: карта так и не освободилась")
			}
			return rep, false
		}
		select {
		case <-ctx.Done():
			return rep, false
		case <-time.After(poll):
		}
	}
}

// currentUser возвращает имя пользователя, под которым идёт прогон.
func currentUser() string {
	out, err := exec.Command("id", "-un").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// parseGPUUsage разбирает ответ nvidia-smi вида "66791, 87": занятая
// видеопамять в МиБ и загрузка в процентах.
func parseGPUUsage(out string) (usedMiB, utilPercent int, ok bool) {
	line := strings.TrimSpace(out)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i] // карта одна; если их станет несколько, смотрим первую
	}
	parts := strings.Split(line, ",")
	if len(parts) < 2 {
		return 0, 0, false
	}
	used, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	util, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return used, util, true
}

// ── Наблюдённый простой карты ────────────────────────────────────────────────

// idleState — с какого момента карта непрерывно свободна.
type idleState struct {
	Since   time.Time `json:"since"`
	Updated time.Time `json:"updated"`
}

// trackIdle обновляет отметку простоя и возвращает, сколько карта свободна.
//
// Отметка живёт в файле, потому что проверки идут разными процессами: тик
// расписания запускает новый olleval каждые четверть часа. Разрыв в наблюдении
// обнуляет счёт — «мы не смотрели» не то же самое, что «было свободно».
func (g *Guard) trackIdle(free bool) time.Duration {
	if g.Root == "" {
		return 0
	}
	path := filepath.Join(g.Root, "state", "gpu_idle.json")
	now := time.Now()

	if !free {
		_ = os.Remove(path)
		return 0
	}

	var st idleState
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	gap := 3 * time.Duration(g.Cfg.Poll)
	if gap < 5*time.Minute {
		gap = 5 * time.Minute
	}
	if st.Since.IsZero() || now.Sub(st.Updated) > gap {
		st.Since = now
	}
	st.Updated = now
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		if b, err := json.Marshal(st); err == nil {
			_ = os.WriteFile(path, append(b, '\n'), 0o644)
		}
	}
	return now.Sub(st.Since)
}

// ── Журнал проверок ──────────────────────────────────────────────────────────

// logCheck дописывает строку в logs/guard.log.
//
// Без этого журнала утренний вопрос «почему ночь не началась» остаётся без
// ответа: тик не оставляет следов, а причина занятости живёт секунды.
func (g *Guard) logCheck(rep GuardReport) {
	if !g.Cfg.LogChecks || g.Root == "" {
		return
	}
	path := filepath.Join(g.Root, "logs", "guard.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	verdict := "свободно"
	if !rep.Free {
		verdict = "занято"
	}
	if rep.NeedServiceStart {
		verdict = "свободно, поднимаю службу"
	}
	service := rep.ServiceState
	if service == "" {
		service = "?"
	}
	line := fmt.Sprintf("%s  %-26s gpu %6d МиБ %3d%% (порог %d%%) служба %-9s простой %-8s",
		time.Now().Format("2006-01-02 15:04:05"), verdict,
		rep.GPUUsedMiB, rep.GPUUtil, rep.UtilLimit, service, round(rep.IdleFor))
	if len(rep.Blocking) > 0 {
		line += " | " + strings.Join(rep.Blocking, "; ")
	}
	for _, n := range rep.Notes {
		// Чужой сеанс ssh прогон не запрещает и висит часами: в журнале это
		// повторялось бы каждую минуту одной и той же строкой.
		if strings.HasPrefix(n, "в системе чужой сеанс") {
			continue
		}
		line += " | " + n
	}
	fmt.Fprintln(f, line)
}
