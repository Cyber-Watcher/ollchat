package nodeprobe

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Память и процессор машины, состояние службы и место на диске.
//
// Всё читается из /proc и у systemd. Отдельно стоит сказать про **поиск
// процесса службы**: его номер берётся у systemd (`systemctl show --property
// MainPID`), а не поиском по строке команды. Поиск по командной строке —
// ошибка, на которую я наступал четырежды: под шаблон попадает и сам ищущий,
// и посторонняя программа с похожим именем в аргументах.

// execRun — запуск внешней команды по умолчанию.
func execRun(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	code := cmd.ProcessState.ExitCode()
	if err != nil {
		return string(out), code, err
	}
	return string(out), code, nil
}

// collectService спрашивает systemd о состоянии службы и её переменных.
//
// Переменные окружения — то, ради чего половина этого пакета и написана:
// OLLAMA_NUM_PARALLEL по сети не виден никак, а от него зависит, сколько
// запросов сервер обрабатывает разом и сколько слотов имеет смысл ему давать.
func (r *Report) collectService(ctx context.Context, o Opts) {
	out, _, err := o.Run(ctx, "systemctl", "show", o.Service,
		"--property=ActiveState", "--property=MainPID",
		"--property=ActiveEnterTimestamp", "--property=Environment")
	if err != nil {
		// Запасной путь: is-active работает и там, где show запрещён политикой.
		if st, _, e2 := o.Run(ctx, "systemctl", "is-active", o.Service); e2 == nil {
			r.Service.State = strings.TrimSpace(st)
			return
		}
		r.miss("service", "не удалось спросить systemd о службе %s: %v", o.Service, err)
		return
	}
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			r.Service.State = val
		case "MainPID":
			r.Service.MainPID, _ = strconv.Atoi(val)
		case "ActiveEnterTimestamp":
			// У погашенной службы systemd пишет «n/a» — это не время,
			// и показывать его как время нельзя.
			if val != "n/a" {
				r.Service.ActiveSince = val
			}
		case "Environment":
			r.Service.Env = parseEnv(val)
		}
	}
	if len(r.Service.Env) == 0 {
		// Переменные могут лежать в EnvironmentFile — тогда systemd их здесь
		// не показывает. Молчать об этом нельзя: пустая карта переменных
		// неотличима от «ничего не задано», а это разные вещи.
		r.miss("service_env", "переменные окружения службы не видны "+
			"(заданы через EnvironmentFile или недоступны)")
	}
}

// parseEnv разбирает строку Environment= от systemd: пары, разделённые
// пробелами, значение может быть в кавычках.
func parseEnv(s string) map[string]string {
	env := map[string]string{}
	for _, f := range splitFields(s) {
		key, val, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		env[key] = strings.Trim(val, `"`)
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

// splitFields режет по пробелам, не разрывая значения в кавычках.
func splitFields(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// collectHost читает память, среднюю загрузку и процесс службы.
func (r *Report) collectHost(ctx context.Context, o Opts) {
	h := Host{Cores: runtime.NumCPU()}
	mem, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		r.miss("host", "/proc/meminfo недоступен: %v", err)
		return
	}
	var swapTotal, swapFree int
	for _, line := range strings.Split(string(mem), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		kib := atoiSafe(strings.TrimSuffix(strings.TrimSpace(val), " kB"))
		switch key {
		case "MemTotal":
			h.RAMTotalMiB = kib / 1024
		case "MemAvailable":
			// Именно MemAvailable: MemFree на машине с кэшем страниц всегда
			// близок к нулю и пугает без причины.
			h.RAMFreeMiB = kib / 1024
		case "SwapTotal":
			swapTotal = kib / 1024
		case "SwapFree":
			swapFree = kib / 1024
		}
	}
	h.SwapUsedMiB = swapTotal - swapFree
	if la, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(la))
		for i := 0; i < 3 && i < len(f); i++ {
			h.LoadAvg[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}
	r.Host = &h

	pid := r.Service.MainPID
	if pid == 0 {
		// Состояние службы могли не спрашивать — спросим только номер.
		if out, _, err := o.Run(ctx, "systemctl", "show", o.Service,
			"--property=MainPID", "--value"); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(out))
		}
	}
	if pid <= 0 {
		return
	}
	p := Proc{PID: pid}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		r.miss("ollama_proc", "процесс %d не читается: %v", pid, err)
		return
	}
	for _, line := range strings.Split(string(status), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "VmRSS":
			p.RSSMiB = atoiSafe(strings.TrimSuffix(strings.TrimSpace(val), " kB")) / 1024
		case "Threads":
			p.Threads = atoiSafe(strings.TrimSpace(val))
		}
	}
	r.Ollama = &p
}

// collectDisk смотрит, сколько места осталось под моделями.
func (r *Report) collectDisk(ctx context.Context, o Opts) {
	path := o.ModelsDir
	if path == "" {
		path = r.Service.Env["OLLAMA_MODELS"]
	}
	if path == "" {
		r.miss("disk", "каталог моделей неизвестен (OLLAMA_MODELS не задан)")
		return
	}
	total, free, err := diskUsage(path)
	if err != nil {
		r.miss("disk", "%s: %v", path, err)
		return
	}
	r.Disk = &Disk{Path: path, TotalMiB: total / (1 << 20), FreeMiB: free / (1 << 20)}
}

// uptime — сколько машина работает.
func uptime() (time.Duration, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, fmt.Errorf("пустой /proc/uptime")
	}
	sec, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(sec) * time.Second, nil
}
