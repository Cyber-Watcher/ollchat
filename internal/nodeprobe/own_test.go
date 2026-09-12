package nodeprobe

import "testing"

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
	rep.markOwnGPUProcs()

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
	rep.markOwnGPUProcs()
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
