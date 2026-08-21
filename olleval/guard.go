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
}

// GuardReport — что показала проверка.
type GuardReport struct {
	Free     bool     `json:"free"`
	Blocking []string `json:"blocking,omitempty"` // из-за чего нельзя начинать
	Notes    []string `json:"notes,omitempty"`    // к сведению: чужие сеансы и прочее

	GPUUsedMiB   int           `json:"gpu_used_mib"`
	GPUUtil      int           `json:"gpu_util"`
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
// Блокирует старт: процессы на видеокарте, загруженная в память модель, свежие
// запросы к Ollama. Чужие сеансы ssh только отмечаются: открытая сессия с
// незакрытым mc — это не работа, а забытое окно, и запрещать по ней ночь значит
// не запускать её никогда.
func (g *Guard) Check(ctx context.Context) GuardReport {
	var rep GuardReport

	if g.Cfg.CheckGPU {
		if out, _, err := g.Run(ctx, "nvidia-smi",
			"--query-compute-apps=pid,used_memory", "--format=csv,noheader"); err == nil {
			if s := strings.TrimSpace(out); s != "" {
				for _, line := range strings.Split(s, "\n") {
					rep.Blocking = append(rep.Blocking, "на видеокарте работает процесс: "+strings.TrimSpace(line))
				}
			}
		} else {
			rep.Notes = append(rep.Notes, "nvidia-smi недоступен: "+err.Error())
		}
	}

	// Занятость карты как таковой: сколько памяти отдано и как она загружена.
	// Обучение модели питоновским скриптом видно именно здесь, а вовсе не через
	// Ollama — её на время такого обучения обычно вообще останавливают.
	gpuBusy := len(rep.Blocking) > 0 // процессы на карте уже посчитаны выше
	if g.Cfg.CheckGPU {
		if out, _, err := g.Run(ctx, "nvidia-smi",
			"--query-gpu=memory.used,utilization.gpu", "--format=csv,noheader,nounits"); err == nil {
			used, util, ok := parseGPUUsage(out)
			if !ok {
				rep.Notes = append(rep.Notes, "не разобрал ответ nvidia-smi: "+strings.TrimSpace(out))
			} else {
				rep.GPUUsedMiB, rep.GPUUtil = used, util
				if g.Cfg.BusyVRAMMiB > 0 && used >= g.Cfg.BusyVRAMMiB {
					rep.Blocking = append(rep.Blocking,
						fmt.Sprintf("на карте занято %d МиБ видеопамяти (порог %d)", used, g.Cfg.BusyVRAMMiB))
					gpuBusy = true
				} else if g.Cfg.BusyUtilPercent > 0 && util >= g.Cfg.BusyUtilPercent {
					rep.Blocking = append(rep.Blocking,
						fmt.Sprintf("карта загружена на %d%% (порог %d%%)", util, g.Cfg.BusyUtilPercent))
					gpuBusy = true
				}
			}
		}
	}

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
	if g.Cfg.CheckService {
		name := g.Cfg.Service
		if name == "" {
			name = "ollama"
		}
		if out, _, err := g.Run(ctx, "systemctl", "is-active", name); err == nil {
			state := strings.TrimSpace(out)
			rep.ServiceState = state
			if state != "active" {
				wait := time.Duration(g.Cfg.IdleBeforeStart)
				switch {
				case gpuBusy || wait <= 0:
					rep.Blocking = append(rep.Blocking, fmt.Sprintf(
						"служба %s не запущена (%s) — её гасят, когда карта нужна под другое", name, state))
				case rep.IdleFor >= wait:
					rep.NeedServiceStart = true
					rep.Notes = append(rep.Notes, fmt.Sprintf(
						"служба %s погашена (%s), но карта свободна уже %s — поднимаю службу сам",
						name, state, round(rep.IdleFor)))
				default:
					rep.Blocking = append(rep.Blocking, fmt.Sprintf(
						"служба %s не запущена (%s); карта свободна %s из нужных %s",
						name, state, round(rep.IdleFor), wait))
				}
			}
		} else {
			rep.Notes = append(rep.Notes, "не удалось спросить состояние службы "+name+": "+err.Error())
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
				rep.Blocking = append(rep.Blocking, fmt.Sprintf("в память сервера загружена модель %s (%.1f ГиБ)",
					m.Name, float64(m.SizeVRAM)/(1<<30)))
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
			log("время ожидания вышло: карта так и не освободилась")
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
	line := fmt.Sprintf("%s  %-26s gpu %6d МиБ %3d%%  служба %-9s простой %-8s",
		time.Now().Format("2006-01-02 15:04:05"), verdict,
		rep.GPUUsedMiB, rep.GPUUtil, service, round(rep.IdleFor))
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
