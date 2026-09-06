package nodeprobe

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// Опрос видеокарты через nvidia-smi.
//
// Почему внешняя программа, а не NVML: библиотека тянет cgo, а с ним —
// компилятор C на каждой машине, где собирается проект. nvidia-smi есть везде,
// где есть драйвер, и его вывод в формате csv стабилен годами.

// collectGPU снимает карты и процессы на них.
func (r *Report) collectGPU(ctx context.Context, o Opts) {
	// Основные показатели — одним запросом. Троттлинг и мощность спрашиваются
	// отдельно: на старых драйверах этих полей нет, и общий запрос упал бы
	// целиком из-за необязательной колонки.
	out, _, err := o.Run(ctx, "nvidia-smi",
		"--query-gpu=index,name,memory.total,memory.used,memory.free,utilization.gpu,temperature.gpu",
		"--format=csv,noheader,nounits")
	if err != nil {
		r.miss("gpu", "nvidia-smi недоступен: %v", err)
		return
	}
	gpus := parseGPUs(out)
	if len(gpus) == 0 {
		r.miss("gpu", "nvidia-smi не назвал ни одной карты")
		return
	}

	// Серия выборок загрузки: одиночный снимок попадает в паузу между двумя
	// токенами чужого ответа и говорит «свободно» посреди чужой работы.
	samples := o.UtilSamples
	if samples < 1 {
		samples = 1
	}
	gap := o.UtilSampleGap
	if gap <= 0 {
		gap = time.Second
	}
	for i := 0; i < len(gpus); i++ {
		gpus[i].Samples = []int{gpus[i].Util}
	}
	for i := 1; i < samples; i++ {
		select {
		case <-ctx.Done():
			r.GPUs = gpus
			return
		case <-time.After(gap):
		}
		out, _, err := o.Run(ctx, "nvidia-smi",
			"--query-gpu=index,utilization.gpu,memory.used", "--format=csv,noheader,nounits")
		if err != nil {
			continue
		}
		for _, s := range parseUtilSamples(out) {
			for j := range gpus {
				if gpus[j].Index != s.index {
					continue
				}
				gpus[j].Samples = append(gpus[j].Samples, s.util)
				if s.util > gpus[j].Util {
					gpus[j].Util = s.util
				}
				if s.used > gpus[j].MemUsed {
					gpus[j].MemUsed = s.used
				}
			}
		}
	}

	// Необязательные показатели. Их отсутствие — не беда и не повод
	// объявлять весь раздел несобранным.
	if out, _, err := o.Run(ctx, "nvidia-smi",
		"--query-gpu=index,power.draw,clocks_throttle_reasons.active",
		"--format=csv,noheader,nounits"); err == nil {
		for _, e := range parseExtras(out) {
			for j := range gpus {
				if gpus[j].Index == e.index {
					gpus[j].PowerW = e.power
					gpus[j].Throttle = e.throttle
				}
			}
		}
	}
	r.GPUs = gpus

	if out, _, err := o.Run(ctx, "nvidia-smi",
		"--query-compute-apps=pid,process_name,used_memory", "--format=csv,noheader,nounits"); err == nil {
		r.GPUProcs = parseGPUProcs(out)
	} else {
		r.miss("gpu_procs", "список процессов на карте недоступен: %v", err)
	}
}

// parseGPUs разбирает строки «индекс, имя, всего, занято, свободно, загрузка, температура».
func parseGPUs(out string) []GPU {
	var gpus []GPU
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := splitCSV(line)
		if len(f) < 6 {
			continue
		}
		g := GPU{Name: f[1]}
		var err error
		if g.Index, err = strconv.Atoi(f[0]); err != nil {
			continue
		}
		g.MemTotal = atoiSafe(f[2])
		g.MemUsed = atoiSafe(f[3])
		g.MemFree = atoiSafe(f[4])
		g.Util = atoiSafe(f[5])
		if len(f) > 6 {
			g.TempC = atoiSafe(f[6])
		}
		gpus = append(gpus, g)
	}
	return gpus
}

type utilSample struct{ index, util, used int }

func parseUtilSamples(out string) []utilSample {
	var res []utilSample
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := splitCSV(line)
		if len(f) < 3 {
			continue
		}
		idx, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		res = append(res, utilSample{index: idx, util: atoiSafe(f[1]), used: atoiSafe(f[2])})
	}
	return res
}

type gpuExtra struct {
	index    int
	power    float64
	throttle string
}

func parseExtras(out string) []gpuExtra {
	var res []gpuExtra
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := splitCSV(line)
		if len(f) < 2 {
			continue
		}
		idx, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		e := gpuExtra{index: idx}
		e.power, _ = strconv.ParseFloat(f[1], 64)
		if len(f) > 2 && !isNotSupported(f[2]) && f[2] != "Not Active" {
			e.throttle = throttleReason(f[2])
		}
		res = append(res, e)
	}
	return res
}

// parseGPUProcs разбирает список процессов, держащих память на карте.
//
// Принадлежность службе определяется по имени программы: у Ollama это
// `ollama` или `ollama_llama_server`. Искать по строке команды нельзя —
// под такой шаблон попадает и сам ищущий, и чужая программа с похожим именем.
func parseGPUProcs(out string) []GPUProc {
	var res []GPUProc
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := splitCSV(line)
		if len(f) < 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		p := GPUProc{PID: pid}
		if len(f) >= 3 {
			p.Name = f[1]
			p.UsedMiB = atoiSafe(f[2])
		} else {
			p.UsedMiB = atoiSafe(f[1])
		}
		base := p.Name
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		p.Ours = strings.HasPrefix(base, "ollama")
		res = append(res, p)
	}
	return res
}

// splitCSV режет строку вывода nvidia-smi по запятым и убирает пробелы.
func splitCSV(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// atoiSafe возвращает число или ноль. «[N/A]» и «Not Supported» встречаются
// в выводе как обычные значения — это не ошибка разбора, а отсутствие датчика.
func atoiSafe(s string) int {
	s = strings.TrimSpace(s)
	if s == "" || isNotSupported(s) {
		return 0
	}
	// Мощность приходит дробной («71.35») даже с nounits.
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func isNotSupported(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	return low == "[n/a]" || low == "n/a" || strings.Contains(low, "not supported")
}

// throttleReason переводит битовую маску причин снижения частоты в слова.
//
// nvidia-smi отдаёт её шестнадцатеричным числом («0x0000000000000020»),
// и в таком виде она бесполезна: наблюдатель, чтобы понять его, требует
// заглянуть в документацию NVIDIA — а смотрят на снимок обычно тогда, когда
// некогда. Биты взяты из nvml.h (nvmlClocksThrottleReason*).
func throttleReason(s string) string {
	s = strings.TrimSpace(s)
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(s), "0x"), 16, 64)
	if err != nil || v == 0 {
		return ""
	}
	// Биты 0x1 (карта простаивает), 0x2 (заданы частоты приложения)
	// и 0x100 (частоты дисплея) — не беда, а обычное положение дел, и в отчёт
	// они не идут вовсе. Показывать «снижение частоты: простой» у карты,
	// загруженной на 99%, значит пугать человека собственной невнимательностью:
	// маска снимается отдельным запросом, на долю секунды позже загрузки.
	known := []struct {
		bit  uint64
		name string
	}{
		{0x4, "предел мощности (программный)"},
		{0x8, "аппаратное снижение"},
		{0x10, "синхронизация с соседней картой"},
		{0x20, "перегрев (программное снижение)"},
		{0x40, "перегрев (аппаратное снижение)"},
		{0x80, "предел мощности (аппаратный)"},
	}
	var out []string
	for _, k := range known {
		if v&k.bit != 0 {
			out = append(out, k.name)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, ", ")
}
