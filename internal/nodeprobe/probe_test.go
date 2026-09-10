package nodeprobe

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeRun изображает внешние команды: ключ — имя программы и первый аргумент,
// значение — вывод. Так снимок проверяется без видеокарты и без systemd.
type fakeRun struct {
	out   map[string]string
	fail  map[string]error
	calls []string
	// utilSeq — очередь ответов на повторные запросы загрузки карты.
	utilSeq []string
}

func (f *fakeRun) run(ctx context.Context, name string, args ...string) (string, int, error) {
	key := name
	if len(args) > 0 {
		key = name + " " + args[0]
	}
	f.calls = append(f.calls, key+" "+strings.Join(args, " "))
	if err, ok := f.fail[key]; ok {
		return "", 1, err
	}
	// Повторные выборки загрузки берутся из очереди, если она задана.
	if name == "nvidia-smi" && len(args) > 0 &&
		strings.Contains(args[0], "utilization.gpu") && !strings.Contains(args[0], "memory.total") {
		if len(f.utilSeq) > 0 {
			out := f.utilSeq[0]
			f.utilSeq = f.utilSeq[1:]
			return out, 0, nil
		}
	}
	if out, ok := f.out[key]; ok {
		return out, 0, nil
	}
	return "", 1, errors.New("команда не задана в тесте: " + key)
}

const gpuQuery = "nvidia-smi --query-gpu=index,name,memory.total,memory.used,memory.free,utilization.gpu,temperature.gpu"

func baseRun() *fakeRun {
	return &fakeRun{out: map[string]string{
		gpuQuery: "0, NVIDIA A100-SXM4-80GB, 81920, 20480, 61440, 3, 41\n",
		"nvidia-smi --query-compute-apps=pid,process_name,used_memory":           "12345, /usr/local/bin/ollama, 20480\n",
		"nvidia-smi --query-gpu=index,power.draw,clocks_throttle_reasons.active": "0, 71.35, Not Active\n",
		"systemctl show": "ActiveState=active\nMainPID=12345\n" +
			"ActiveEnterTimestamp=Sat 2026-09-06 03:00:00 MSK\n" +
			`Environment=OLLAMA_NUM_PARALLEL=4 OLLAMA_MODELS=/srv/models OLLAMA_KEEP_ALIVE=30m` + "\n",
		"journalctl -u": "сен 06 03:01 srv ollama[1]: WARN model architecture does not currently support parallel requests\n" +
			"сен 06 03:02 srv ollama[1]: INFO GET /api/tags 200\n",
		"w -h": "admin    pts/0    10:00    1.00s  ollama ps\n",
	}}
}

func TestSnapshotGPU(t *testing.T) {
	f := baseRun()
	rep := Snapshot(context.Background(), Opts{Run: f.run, Want: Sections{GPU: true}})

	if len(rep.GPUs) != 1 {
		t.Fatalf("карт %d, ожидалась одна: %+v", len(rep.GPUs), rep.Missing)
	}
	g := rep.GPUs[0]
	if g.Name != "NVIDIA A100-SXM4-80GB" || g.MemTotal != 81920 || g.MemUsed != 20480 || g.TempC != 41 {
		t.Errorf("карта разобрана неверно: %+v", g)
	}
	if g.PowerW < 71 || g.PowerW > 72 {
		t.Errorf("мощность %v", g.PowerW)
	}
	if g.Throttle != "" {
		t.Errorf("«Not Active» не должно попадать в троттлинг: %q", g.Throttle)
	}
	if len(rep.GPUProcs) != 1 || !rep.GPUProcs[0].Ours || rep.GPUProcs[0].PID != 12345 {
		t.Errorf("процессы на карте: %+v", rep.GPUProcs)
	}
	if len(rep.Foreign()) != 0 {
		t.Errorf("процесс ollama посчитан чужим: %+v", rep.Foreign())
	}
}

// Чужое обучение на карте — то, ради чего наблюдатель и ставится на сервер:
// по сети его не видно никак.
func TestSnapshotForeignProcess(t *testing.T) {
	f := baseRun()
	f.out["nvidia-smi --query-compute-apps=pid,process_name,used_memory"] =
		"12345, /usr/local/bin/ollama, 20480\n999, /usr/bin/python3, 40960\n"
	rep := Snapshot(context.Background(), Opts{Run: f.run, Want: Sections{GPU: true}})

	foreign := rep.Foreign()
	if len(foreign) != 1 || foreign[0].PID != 999 || foreign[0].UsedMiB != 40960 {
		t.Fatalf("чужой процесс не распознан: %+v", rep.GPUProcs)
	}
}

// Наибольшая выборка серии, а не последняя: одиночный снимок попадает в паузу
// между токенами чужого ответа и говорит «свободно» посреди чужой работы.
func TestSnapshotTakesLargestSample(t *testing.T) {
	f := baseRun()
	f.utilSeq = []string{"0, 97, 30000\n", "0, 2, 20480\n"}
	rep := Snapshot(context.Background(), Opts{Run: f.run, Want: Sections{GPU: true},
		UtilSamples: 3, UtilSampleGap: time.Millisecond})

	if len(rep.GPUs) != 1 {
		t.Fatal("карта не собралась")
	}
	if got := rep.GPUs[0].Util; got != 97 {
		t.Errorf("загрузка %d%%, ожидалось 97%% — берётся наибольшая выборка", got)
	}
	if got := rep.GPUs[0].MemUsed; got != 30000 {
		t.Errorf("занятая память %d, ожидалось 30000", got)
	}
	if n := len(rep.GPUs[0].Samples); n != 3 {
		t.Errorf("выборок %d, ожидалось 3", n)
	}
}

// Нет nvidia-smi — честное «не знаю», а не нули: ноль в поле «занято памяти»
// неотличим от «карта свободна».
func TestSnapshotWithoutNvidiaSmi(t *testing.T) {
	f := baseRun()
	f.fail = map[string]error{gpuQuery: errors.New("exec: nvidia-smi not found")}
	rep := Snapshot(context.Background(), Opts{Run: f.run, Want: Sections{GPU: true}})

	if len(rep.GPUs) != 0 {
		t.Fatalf("карты появились из ниоткуда: %+v", rep.GPUs)
	}
	if rep.Has("gpu") {
		t.Fatal("несобранный раздел не помечен в Missing")
	}
	var reason string
	for _, m := range rep.Missing {
		if m.Section == "gpu" {
			reason = m.Reason
		}
	}
	if !strings.Contains(reason, "nvidia-smi") {
		t.Errorf("причина невнятная: %q", reason)
	}
}

// Переменные окружения службы — то, чего по сети не видно вовсе.
func TestSnapshotService(t *testing.T) {
	f := baseRun()
	rep := Snapshot(context.Background(), Opts{Run: f.run, Want: Sections{Service: true}})

	if rep.Service.State != "active" || rep.Service.MainPID != 12345 {
		t.Fatalf("служба разобрана неверно: %+v", rep.Service)
	}
	if got := rep.Service.Slots(); got != 4 {
		t.Errorf("слотов %d, ожидалось 4 (OLLAMA_NUM_PARALLEL)", got)
	}
	if rep.Service.Env["OLLAMA_MODELS"] != "/srv/models" {
		t.Errorf("переменные: %+v", rep.Service.Env)
	}
}

// Переменные могут лежать в EnvironmentFile — тогда их не видно, и молчать
// об этом нельзя: пустая карта неотличима от «ничего не задано».
func TestSnapshotServiceWithoutEnv(t *testing.T) {
	f := baseRun()
	f.out["systemctl show"] = "ActiveState=active\nMainPID=12345\nEnvironment=\n"
	rep := Snapshot(context.Background(), Opts{Run: f.run, Want: Sections{Service: true}})

	if rep.Has("service_env") {
		t.Error("отсутствие переменных не помечено")
	}
	if rep.Service.State != "active" {
		t.Errorf("состояние службы потеряно: %+v", rep.Service)
	}
}

// Запасной путь: systemctl show запрещён политикой, is-active работает.
func TestSnapshotServiceFallback(t *testing.T) {
	f := baseRun()
	f.fail = map[string]error{"systemctl show": errors.New("Access denied")}
	f.out["systemctl is-active"] = "active\n"
	rep := Snapshot(context.Background(), Opts{Run: f.run, Want: Sections{Service: true}})

	if rep.Service.State != "active" {
		t.Fatalf("запасной путь не сработал: %+v, %+v", rep.Service, rep.Missing)
	}
}

// Вытеснение модели в оперативную память: сколько именно ушло и виден ли факт.
func TestSnapshotModels(t *testing.T) {
	ps := func(ctx context.Context) ([]RunningModel, error) {
		return []RunningModel{
			{Name: "qwen3.8:latest", Size: 20 << 30, SizeVRAM: 20 << 30, ContextLength: 4096},
			{Name: "glm-4.7-flash:q8_0", Size: 40 << 30, SizeVRAM: 10 << 30},
		}, nil
	}
	rep := Snapshot(context.Background(), Opts{Run: baseRun().run, PS: ps, Want: Sections{Models: true}})

	if len(rep.Models) != 2 {
		t.Fatalf("моделей %d", len(rep.Models))
	}
	if rep.Models[0].Evicted() {
		t.Error("модель целиком на карте объявлена вытесненной")
	}
	ev := rep.Evicted()
	if len(ev) != 1 || ev[0].Name != "glm-4.7-flash:q8_0" {
		t.Fatalf("вытеснение не поймано: %+v", rep.Models)
	}
	if ev[0].SizeRAM != 30<<30 || ev[0].VRAMPct != 25 {
		t.Errorf("разложение памяти неверное: в ОЗУ %d, на карте %d%%", ev[0].SizeRAM, ev[0].VRAMPct)
	}
}

// Журнал отдаётся не целиком, а известными бедами: за пятнадцать минут работы
// Ollama пишет тысячи строк о запросах.
func TestSnapshotJournal(t *testing.T) {
	rep := Snapshot(context.Background(), Opts{Run: baseRun().run,
		Want: Sections{Journal: true}, JournalWindow: 15 * time.Minute})

	if len(rep.Journal) != 1 {
		t.Fatalf("строк журнала %d, ожидалась одна: %+v", len(rep.Journal), rep.Journal)
	}
	if rep.Journal[0].Kind != "parallel" {
		t.Errorf("беда распознана как %q", rep.Journal[0].Kind)
	}
}

func TestJournalKinds(t *testing.T) {
	cases := map[string]string{
		"model architecture does not currently support parallel requests": "parallel",
		"CUDA error: out of memory":                                       "cuda",
		"ggml_backend_cuda_buffer_type_alloc_buffer: out of memory":       "oom",
		"unloading model qwen3.8":                                         "unload",
		"GET /api/tags 200":                                               "",
	}
	for line, want := range cases {
		if got := journalKind(line); got != want {
			t.Errorf("%q → %q, ожидалось %q", line, got, want)
		}
	}
}

// Свой сеанс не считается чужим.
func TestSnapshotSessions(t *testing.T) {
	f := baseRun()
	t.Setenv("USER", "admin")
	rep := Snapshot(context.Background(), Opts{Run: f.run, Want: Sections{Sessions: true}})
	if len(rep.Sessions) != 0 {
		t.Errorf("свой сеанс объявлен чужим: %+v", rep.Sessions)
	}

	t.Setenv("USER", "someone-else")
	rep = Snapshot(context.Background(), Opts{Run: f.run, Want: Sections{Sessions: true}})
	if len(rep.Sessions) != 1 {
		t.Errorf("чужой сеанс не замечен: %+v", rep.Sessions)
	}
}

// Пустой набор разделов ничего не собирает: снимок стоит секунд, и брать
// его целиком ради одного числа незачем.
func TestSnapshotCollectsOnlyWhatAsked(t *testing.T) {
	f := baseRun()
	Snapshot(context.Background(), Opts{Run: f.run, Want: Sections{Service: true}})
	for _, c := range f.calls {
		if strings.HasPrefix(c, "nvidia-smi") {
			t.Fatalf("спрошена карта, хотя просили только службу: %q", c)
		}
	}
}

func TestParseGPUsMultiCard(t *testing.T) {
	out := "0, NVIDIA A100, 81920, 20480, 61440, 3, 41\n1, NVIDIA GeForce RTX 3090, 24576, 17700, 6876, 88, 72\n"
	gpus := parseGPUs(out)
	if len(gpus) != 2 {
		t.Fatalf("карт %d", len(gpus))
	}
	if gpus[1].Index != 1 || gpus[1].MemFree != 6876 || gpus[1].Util != 88 {
		t.Errorf("вторая карта: %+v", gpus[1])
	}
}

// «[N/A]» и «Not Supported» — обычные значения в выводе, а не ошибка разбора.
func TestAtoiSafeHandlesMissingSensors(t *testing.T) {
	for _, s := range []string{"[N/A]", "N/A", "Not Supported", "", "  "} {
		if got := atoiSafe(s); got != 0 {
			t.Errorf("%q → %d", s, got)
		}
	}
	if got := atoiSafe("71.35"); got != 71 {
		t.Errorf("дробное значение: %d", got)
	}
}

// Троттлинг: настоящие ограничения переводятся в слова, а «простой» и прочее
// обычное положение дел в отчёт не идут.
func TestThrottleReason(t *testing.T) {
	cases := map[string]string{
		"0x0000000000000001": "", // карта простаивает — не беда
		"0x0000000000000002": "", // заданы частоты приложения
		"0x0000000000000100": "", // частоты дисплея
		"0x0000000000000020": "перегрев (программное снижение)",
		"0x0000000000000024": "предел мощности (программный), перегрев (программное снижение)",
		"0x0000000000000000": "",
		"мусор":              "",
	}
	for in, want := range cases {
		if got := throttleReason(in); got != want {
			t.Errorf("%s → %q, ожидалось %q", in, got, want)
		}
	}
}

// nvidia-smi ответил, но карт в ответе не разобралось: процессы на карте
// всё равно спрашиваются. Это отдельный факт, и без него вызывающий, лишённый
// замера загрузки, не смог бы применить строгое правило «любой процесс — занято».
func TestSnapshotProcsWithoutCards(t *testing.T) {
	f := baseRun()
	f.out[gpuQuery] = "нет ответа\n"
	f.out["nvidia-smi --query-compute-apps=pid,process_name,used_memory"] = "999, /usr/bin/python3, 40960\n"
	rep := Snapshot(context.Background(), Opts{Run: f.run, Want: Sections{GPU: true}})

	if rep.Has("gpu") || len(rep.GPUs) != 0 {
		t.Fatalf("карты появились из неразбираемого ответа: %+v", rep.GPUs)
	}
	if len(rep.GPUProcs) != 1 || rep.GPUProcs[0].PID != 999 {
		t.Errorf("процессы на карте не собраны без карт: %+v (%+v)", rep.GPUProcs, rep.Missing)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "utilization.gpu,memory.used") || strings.Contains(c, "power.draw") {
			t.Errorf("без карт спрошены выборки и мощность: %q", c)
		}
	}
}

// Без nvidia-smi процессы не спрашиваются вовсе: спрашивать нечем.
func TestSnapshotNoProcsWithoutNvidiaSmi(t *testing.T) {
	f := baseRun()
	f.fail = map[string]error{gpuQuery: errors.New("exec: nvidia-smi not found")}
	Snapshot(context.Background(), Opts{Run: f.run, Want: Sections{GPU: true}})
	for _, c := range f.calls {
		if strings.Contains(c, "compute-apps") {
			t.Errorf("процессы спрошены у отсутствующего nvidia-smi: %q", c)
		}
	}
}

// Запросы к службе считаются, а не хранятся: их тысячи, а нужно одно число.
func TestSnapshotCountsRequests(t *testing.T) {
	f := baseRun()
	f.out["journalctl -u"] = "ollama[1]: [GIN] POST /api/chat 200\n" +
		"ollama[1]: [GIN] GET /api/tags 200\n" +
		"ollama[1]: [GIN] POST /api/generate 200\n" +
		"ollama[1]: WARN CUDA error: out of memory\n"
	rep := Snapshot(context.Background(), Opts{Run: f.run,
		Want: Sections{Journal: true}, JournalWindow: 15 * time.Minute})

	if rep.Requests != 2 {
		t.Errorf("запросов %d, ожидалось 2", rep.Requests)
	}
	if len(rep.Journal) != 1 || rep.Journal[0].Kind != "cuda" {
		t.Errorf("строки о запросах попали в журнал бед: %+v", rep.Journal)
	}
}

// Под systemd-таймером USER и LOGNAME бывают пусты — имя берётся по uid.
func TestCurrentUserFallsBackToUID(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("LOGNAME", "")
	if got := currentUser(); got == "" {
		t.Error("без USER и LOGNAME имя пользователя пустое — свои сеансы считались бы чужими")
	}
}
