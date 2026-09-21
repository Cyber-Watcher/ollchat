package nodeprobe

import (
	"os"
	"strconv"
	"strings"
)

// Опознание своих процессов на карте — по родству со службой, а не по имени.
//
// **Зачем (12.09.2026).** Раньше признак «свой» ставился по имени программы:
// `strings.HasPrefix(base, "ollama")`. Но счётчики моделей Ollama запускает
// сама, и называются они `llama-server` (`/usr/local/lib/ollama/llama-server`),
// поэтому наблюдатель объявлял чужой работой нашу же сборку: на стенде в списке
// оказались два процесса службы (qwen3.8 и bge-m3) и наш реранкер, а сборка,
// поверив им, отказалась считать. Имя как признак остаётся запасным — родство
// надёжнее: процесс, чей предок главный процесс службы, принадлежит ей.
//
// Смотреть на /proc можно только на самой машине — наблюдатель там и работает.
//
// **Чего родство не ловит (21.09.2026).** Наш реранкер `bge-reranker-v2-m3`
// поднят отдельным контейнером (llama.cpp с `--reranking`), и на карте он виден
// как `/app/llama-server` (~1 460 МиБ). Службе Ollama он не родня и `ollama*`
// не зовётся, поэтому попадал в «чужое на карте», а строка хода сборки пугала
// посторонней работой, которой нет. Поведения это не меняло (после 12.09
// наблюдатель на сборку не влияет вовсе), но читать такую строку нельзя было
// иначе как ложь. Поэтому у опознания есть третий, заданный человеком признак:
// список путей своих процессов (`ollnode --ours`, Opts.OwnProcs). Умолчание
// пусто: что своё на ЭТОЙ машине, знает только тот, кто её настраивал.

// markOwnGPUProcs помечает своими процессы карты: произошедшие от службы
// и совпавшие со списком своих путей. Зовётся после сбора раздела службы:
// без её главного номера сверять не с чем.
func (r *Report) markOwnGPUProcs(own []string) {
	for i := range r.GPUProcs {
		if r.GPUProcs[i].Ours {
			continue
		}
		if matchesOwn(r.GPUProcs[i].Name, own) {
			r.GPUProcs[i].Ours = true
		}
	}
	main := r.Service.MainPID
	if main <= 0 {
		return
	}
	for i := range r.GPUProcs {
		if r.GPUProcs[i].Ours {
			continue
		}
		if descendantOf(r.GPUProcs[i].PID, main) {
			r.GPUProcs[i].Ours = true
		}
	}
}

// matchesOwn — путь процесса содержит одну из заданных человеком подстрок.
// Пустые строки списка пропускаются: иначе «» совпало бы с чем угодно.
func matchesOwn(name string, own []string) bool {
	if name == "" {
		return false
	}
	for _, o := range own {
		o = strings.TrimSpace(o)
		if o != "" && strings.Contains(name, o) {
			return true
		}
	}
	return false
}

// descendantOf — процесс pid произошёл от main или сам им является.
//
// Глубина ограничена восемью шагами: цепочка родителей на Linux короткая,
// а зацикливаться на испорченном /proc наблюдателю нельзя — он служба.
func descendantOf(pid, main int) bool {
	for i := 0; i < 8 && pid > 1; i++ {
		if pid == main {
			return true
		}
		pid = procParent(pid)
	}
	return false
}

// procParent — родитель процесса по /proc/<pid>/stat; 0, если не прочиталось.
//
// Переменная, а не функция, ради проверок: тест подменяет её цепочкой
// родителей и не зависит от живых процессов машины.
var procParent = func(pid int) int {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// Второе поле — имя программы в скобках, и в нём бывают и пробелы,
	// и сами скобки. Поэтому разбор начинается после ПОСЛЕДНЕЙ скобки.
	s := string(raw)
	i := strings.LastIndex(s, ")")
	if i < 0 || i+2 >= len(s) {
		return 0
	}
	f := strings.Fields(s[i+1:])
	// f[0] — состояние процесса, f[1] — номер родителя.
	if len(f) < 2 {
		return 0
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return 0
	}
	return ppid
}
