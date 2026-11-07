package nodeprobe

import (
	"strings"
	"testing"
)

// Свой процесс опознаётся по родству со службой, а не по имени программы.
//
// Разбор беды 12.09.2026: на стенде карту держали три процесса — два счётчика
// моделей самой Ollama (`/usr/local/lib/ollama/llama-server`, дети главного
// процесса службы) и наш реранкер. По имени все трое выглядели чужими, сборка
// поверила наблюдателю и отменила заход. Теперь имя — запасной признак.
func TestMarkOwnGPUProcsByParent(t *testing.T) {
	old := procParent
	t.Cleanup(func() { procParent = old })
	// 604213 → 145766 (главный процесс службы); 999 → 1 (посторонний).
	procParent = func(pid int) int {
		switch pid {
		case 604213, 354448:
			return 145766
		case 145766:
			return 1
		default:
			return 1
		}
	}

	rep := Report{
		Service: Service{Name: "ollama", MainPID: 145766},
		GPUProcs: []GPUProc{
			{PID: 604213, Name: "/usr/local/lib/ollama/llama-server", UsedMiB: 31560},
			{PID: 354448, Name: "/usr/local/lib/ollama/llama-server", UsedMiB: 1162},
			{PID: 999, Name: "/usr/bin/python3", UsedMiB: 20000},
		},
	}
	rep.markOwnGPUProcs(nil)

	for _, p := range rep.GPUProcs[:2] {
		if !p.Ours {
			t.Errorf("счётчик модели службы (pid %d) посчитан чужим", p.PID)
		}
	}
	foreign := rep.Foreign()
	if len(foreign) != 1 || foreign[0].PID != 999 {
		t.Fatalf("чужим должен остаться только посторонний процесс: %+v", foreign)
	}
}

// Без номера главного процесса службы разметка молчит: гадать не о чем.
func TestMarkOwnGPUProcsWithoutService(t *testing.T) {
	old := procParent
	t.Cleanup(func() { procParent = old })
	procParent = func(int) int {
		t.Error("без главного процесса службы /proc спрашивать незачем")
		return 0
	}

	rep := Report{GPUProcs: []GPUProc{{PID: 604213, Name: "llama-server"}}}
	rep.markOwnGPUProcs(nil)
	if rep.GPUProcs[0].Ours {
		t.Error("процесс помечен своим без всяких на то оснований")
	}
}

// Цепочка родителей не зацикливает наблюдателя: он служба и висеть не вправе.
func TestDescendantOfStopsOnLoop(t *testing.T) {
	old := procParent
	t.Cleanup(func() { procParent = old })
	procParent = func(pid int) int {
		if pid == 2 {
			return 3
		}
		return 2 // 2 → 3 → 2 → 3 …
	}
	if descendantOf(2, 145766) {
		t.Error("зацикленная цепочка родителей выдана за родство")
	}
}

// Свой процесс, которого служба не запускала: наш реранкер поднят контейнером
// и виден как /app/llama-server. Родство его не ловит — ловит список путей.
// llama.cpp сборки графа на сервере 2×3090 поднят своей службой мимо Ollama
// (журнал очереди 06.11.2026: «чужое на карте: llama-server (20256 МиБ)» и
// «слотов 4» при шести слотах узла). Каталог движков в списке своих делает его
// своим, а слоты в строке названы слотами службы, а не узла.
func TestLineNamesServiceSlotsAndOwnLlamaCpp(t *testing.T) {
	old := procParent
	t.Cleanup(func() { procParent = old })
	procParent = func(int) int { return 1 }

	rep := Report{
		Service:  Service{Name: "ollama", MainPID: 4242, Env: map[string]string{"OLLAMA_NUM_PARALLEL": "4"}},
		GPUs:     []GPU{{Name: "NVIDIA GeForce RTX 3090", MemUsed: 21252, MemTotal: 24576}},
		GPUProcs: []GPUProc{{PID: 777, Name: "/opt/engines/llama.cpp-src/build-no-nccl/bin/llama-server", UsedMiB: 20256}},
	}
	if busy := rep.Busy(0); !strings.Contains(busy, "llama-server") {
		t.Fatalf("без списка своих llama.cpp обязан быть чужим: %q", busy)
	}
	rep.markOwnGPUProcs([]string{"/usr/bin/ollama", "/opt/engines/llama.cpp-src/"})
	if busy := rep.Busy(0); busy != "" {
		t.Errorf("llama.cpp из списка своих всё ещё чужой: %q", busy)
	}
	if line := rep.Line(); !strings.Contains(line, "слотов службы ollama 4") {
		t.Errorf("слоты не названы слотами службы: %q", line)
	}
	if line := (&Report{Service: Service{Env: map[string]string{"OLLAMA_NUM_PARALLEL": "2"}}}).Line(); !strings.Contains(line, "слотов 2") {
		t.Errorf("без имени службы — прежнее «слотов N»: %q", line)
	}
}

func TestMarkOwnGPUProcsByOwnList(t *testing.T) {
	old := procParent
	t.Cleanup(func() { procParent = old })
	procParent = func(int) int { return 1 } // ничьё родство не подтверждается

	rep := Report{
		Service: Service{Name: "ollama", MainPID: 145766},
		GPUProcs: []GPUProc{
			{PID: 2415797, Name: "/app/llama-server", UsedMiB: 1460},
			{PID: 999, Name: "/usr/bin/python3", UsedMiB: 20000},
		},
	}
	rep.markOwnGPUProcs([]string{" ", "/app/llama-server"})

	if !rep.GPUProcs[0].Ours {
		t.Error("реранкер из списка своих посчитан чужим")
	}
	foreign := rep.Foreign()
	if len(foreign) != 1 || foreign[0].PID != 999 {
		t.Fatalf("чужим должен остаться только посторонний процесс: %+v", foreign)
	}
	if busy := rep.Busy(0); strings.Contains(busy, "llama-server") {
		t.Errorf("строка о занятости всё ещё поминает наш реранкер: %q", busy)
	}
	// Пустая строка в списке не делает своим кого попало.
	rep2 := Report{GPUProcs: []GPUProc{{PID: 999, Name: "/usr/bin/python3", UsedMiB: 20000}}}
	rep2.markOwnGPUProcs([]string{"", "   "})
	if rep2.GPUProcs[0].Ours {
		t.Error("пустая строка списка сделала чужой процесс своим")
	}
}
