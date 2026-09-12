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

// markOwnGPUProcs помечает своими процессы карты, произошедшие от службы.
// Зовётся после сбора раздела службы: без её главного номера сверять не с чем.
func (r *Report) markOwnGPUProcs() {
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
