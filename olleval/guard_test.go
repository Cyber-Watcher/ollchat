package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
	"net/http"
	"net/http/httptest"
)

// fakeOllama поднимает сервер, отвечающий на /api/ps заданным телом.
// testGuardCfg — все проверки включены, окно журнала задано явно.
func testGuardCfg(window time.Duration) GuardCfg {
	return GuardCfg{
		FreeChecks: 1, Poll: Duration(time.Minute), JournalWindow: Duration(window),
		CheckGPU: true, CheckPS: true, CheckJournal: true, CheckSessions: true,
	}
}

func fakeOllama(t *testing.T, psBody string) *ollama.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(psBody))
	}))
	t.Cleanup(srv.Close)
	return ollama.New(srv.URL, 5*time.Second, 5*time.Second, nil)
}

func TestGuardСвободнаяКарта(t *testing.T) {
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: testGuardCfg(15 * time.Minute),
		Run: func(ctx context.Context, name string, args ...string) (string, int, error) {
			switch name {
			case "nvidia-smi":
				return "\n", 0, nil
			case "sudo":
				return "-- No entries --\n", 0, nil
			case "w":
				return "", 0, nil
			}
			return "", 0, nil
		}}
	rep := g.Check(context.Background())
	if !rep.Free {
		t.Errorf("карта названа занятой: %v", rep.Blocking)
	}
}

func TestGuardЗанятаяКарта(t *testing.T) {
	cases := map[string]struct {
		ps  string
		smi string
		log string
	}{
		"процесс на карте": {`{"models":[]}`, "492677, 25138 MiB\n", ""},
		"модель в памяти":  {`{"models":[{"name":"qwen3.5:122b","size_vram":80000000000}]}`, "", ""},
		"свежие запросы":   {`{"models":[]}`, "", "POST /api/chat 200\nPOST /api/chat 200\n"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			g := &Guard{Client: fakeOllama(t, c.ps), Cfg: testGuardCfg(15 * time.Minute),
				Run: func(ctx context.Context, cmd string, args ...string) (string, int, error) {
					switch cmd {
					case "nvidia-smi":
						return c.smi, 0, nil
					case "sudo":
						return c.log, 0, nil
					}
					return "", 0, nil
				}}
			rep := g.Check(context.Background())
			if rep.Free {
				t.Fatalf("карта названа свободной, хотя %s", name)
			}
			if len(rep.Blocking) == 0 {
				t.Error("причина занятости не названа")
			}
		})
	}
}

// Чужой сеанс ssh не запрещает ночь: забытое окно mc — это не работа.
func TestGuardЧужойСеансТолькоКСведению(t *testing.T) {
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: testGuardCfg(time.Minute),
		Run: func(ctx context.Context, cmd string, args ...string) (string, int, error) {
			if cmd == "w" {
				return "admvekas pts/0    10.0.0.46        Thu03   32:18   0.14s   ?   tmux\n", 0, nil
			}
			return "", 0, nil
		}}
	rep := g.Check(context.Background())
	if !rep.Free {
		t.Errorf("чужой сеанс запретил прогон: %v", rep.Blocking)
	}
	if len(rep.Notes) == 0 || !strings.Contains(strings.Join(rep.Notes, " "), "admvekas") {
		t.Errorf("чужой сеанс не отмечен: %v", rep.Notes)
	}
}

// Старт только после нескольких свободных проверок подряд: человек может
// сесть за стенд в полночь и проработать десять минут, а одна удачная проверка
// в паузе между его запросами отобрала бы у него память.
func TestWaitFreeТребуетСвободныхПодряд(t *testing.T) {
	states := []string{"занято", "свободно", "занято", "свободно", "свободно", "свободно"}
	var i int
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: testGuardCfg(time.Minute),
		Run: func(ctx context.Context, cmd string, args ...string) (string, int, error) {
			// Состояние сменяется по одному вопросу за проверку — по списку
			// процессов; второй вопрос к nvidia-smi (память и загрузка) идёт
			// в той же проверке и счётчик двигать не должен.
			if cmd != "nvidia-smi" || len(args) == 0 || !strings.Contains(args[0], "compute-apps") {
				return "", 0, nil
			}
			state := states[min(i, len(states)-1)]
			i++
			if state == "занято" {
				return "1234, 25138 MiB\n", 0, nil
			}
			return "", 0, nil
		}}

	var lines []string
	rep, ok := g.WaitFree(context.Background(), 3, time.Millisecond, time.Now().Add(time.Minute),
		func(s string) { lines = append(lines, s) })
	if !ok || !rep.Free {
		t.Fatalf("старт не состоялся: %+v", rep)
	}
	if i != len(states) {
		t.Errorf("проверок сделано %d, ожидалось %d — счётчик не сбрасывался на занятости", i, len(states))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "сброшен") {
		t.Error("сброс счётчика не отмечен в журнале — утром будет непонятно, почему ночь началась поздно")
	}
}

// Если карта так и не освободилась, ночь пропускается, а не ждёт вечно.
func TestWaitFreeСдаётсяПоВремени(t *testing.T) {
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: testGuardCfg(time.Minute),
		Run: func(ctx context.Context, cmd string, args ...string) (string, int, error) {
			if cmd == "nvidia-smi" {
				return "1234, 25138 MiB\n", 0, nil
			}
			return "", 0, nil
		}}
	_, ok := g.WaitFree(context.Background(), 3, time.Millisecond, time.Now().Add(5*time.Millisecond), func(string) {})
	if ok {
		t.Error("ожидание закончилось стартом, хотя карта всё время занята")
	}
}

// Без ожидания guard обязан ответить сразу: проверить и сказать. Иначе
// команда без флагов молча висит на занятой карте.
func TestWaitFreeБезОжиданияОтвечаетСразу(t *testing.T) {
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: testGuardCfg(time.Minute),
		Run: func(ctx context.Context, cmd string, args ...string) (string, int, error) {
			if cmd == "nvidia-smi" {
				return "1234, 25138 MiB\n", 0, nil
			}
			return "", 0, nil
		}}
	began := time.Now()
	_, ok := g.WaitFree(context.Background(), 3, time.Minute, time.Now(), func(string) {})
	if ok {
		t.Error("занятая карта названа свободной")
	}
	if time.Since(began) > 5*time.Second {
		t.Errorf("проверка заняла %s — команда без ожидания не должна ждать", time.Since(began))
	}
}

// fakeRun разводит команды по назначению: два разных вопроса к nvidia-smi,
// состояние службы, журнал и сеансы.
type fakeRun struct {
	computeApps string // ответ на --query-compute-apps
	gpuUsage    string // ответ на --query-gpu
	service     string // ответ systemctl is-active
	journal     string
}

func (f fakeRun) run(ctx context.Context, name string, args ...string) (string, int, error) {
	switch {
	case name == "nvidia-smi" && len(args) > 0 && strings.Contains(args[0], "compute-apps"):
		return f.computeApps, 0, nil
	case name == "nvidia-smi":
		return f.gpuUsage, 0, nil
	case name == "systemctl":
		return f.service, 0, nil
	case name == "sudo":
		return f.journal, 0, nil
	}
	return "", 0, nil
}

func полнаяПроверка() GuardCfg {
	c := testGuardCfg(15 * time.Minute)
	c.CheckService = true
	c.Service = "ollama"
	c.BusyVRAMMiB = 1024
	c.BusyUtilPercent = 20
	c.LogChecks = true
	return c
}

// Обучение модели питоновским скриптом: Ollama на это время гасят, чтобы
// освободить видеопамять. Прогон в такой момент поднял бы службу за спиной
// у человека и отобрал карту посреди обучения.
func TestGuardОбучениеНаКартеСОстановленнойOllama(t *testing.T) {
	g := &Guard{
		Client: fakeOllama(t, `{"models":[]}`),
		Cfg:    полнаяПроверка(),
		Run: fakeRun{
			computeApps: "1644843, python, 66780 MiB\n",
			gpuUsage:    "66791, 87\n",
			service:     "inactive\n",
		}.run,
	}
	rep := g.Check(context.Background())
	if rep.Free {
		t.Fatal("карта названа свободной во время обучения")
	}
	all := strings.Join(rep.Blocking, " | ")
	for _, want := range []string{"python", "66791 МиБ", "служба ollama не запущена"} {
		if !strings.Contains(all, want) {
			t.Errorf("в причинах нет %q: %s", want, all)
		}
	}
}

// Тот же случай, но процессов на карте не видно — так бывает в контейнерах,
// при чужих правах и на MIG. Занятая память и загрузка видны всегда.
func TestGuardЗанятаяПамятьБезВидимыхПроцессов(t *testing.T) {
	g := &Guard{
		Client: fakeOllama(t, `{"models":[]}`),
		Cfg:    полнаяПроверка(),
		Run:    fakeRun{computeApps: "", gpuUsage: "40000, 5\n", service: "active\n"}.run,
	}
	rep := g.Check(context.Background())
	if rep.Free {
		t.Fatal("40 ГиБ занятой памяти не остановили прогон")
	}
	if !strings.Contains(strings.Join(rep.Blocking, " "), "40000 МиБ") {
		t.Errorf("причина названа неверно: %v", rep.Blocking)
	}
}

// Загрузка без заметной памяти — тоже занятость: кто-то считает на карте.
func TestGuardЗагруженнаяКартаБезПамяти(t *testing.T) {
	g := &Guard{
		Client: fakeOllama(t, `{"models":[]}`),
		Cfg:    полнаяПроверка(),
		Run:    fakeRun{gpuUsage: "200, 75\n", service: "active\n"}.run,
	}
	if rep := g.Check(context.Background()); rep.Free {
		t.Error("75% загрузки не остановили прогон")
	}
}

// Пустая карта с работающей службой — можно начинать. Немного занятой памяти
// (драйвер, буферы) порогом не считается, иначе прогон не начался бы никогда.
func TestGuardПустаяКартаСРаботающейСлужбой(t *testing.T) {
	g := &Guard{
		Client: fakeOllama(t, `{"models":[]}`),
		Cfg:    полнаяПроверка(),
		Run:    fakeRun{gpuUsage: "300, 0\n", service: "active\n"}.run,
	}
	rep := g.Check(context.Background())
	if !rep.Free {
		t.Errorf("свободная карта названа занятой: %v", rep.Blocking)
	}
}

func TestParseGPUUsage(t *testing.T) {
	cases := map[string]struct {
		used, util int
		ok         bool
	}{
		"66791, 87":       {66791, 87, true},
		" 0, 0 ":          {0, 0, true},
		"300, 5\n400, 10": {300, 5, true}, // карт несколько — смотрим первую
		"[N/A], [N/A]":    {0, 0, false},
		"мусор":           {0, 0, false},
	}
	for in, want := range cases {
		used, util, ok := parseGPUUsage(in)
		if used != want.used || util != want.util || ok != want.ok {
			t.Errorf("parseGPUUsage(%q) = %d, %d, %v; ожидалось %d, %d, %v",
				in, used, util, ok, want.used, want.util, want.ok)
		}
	}
}

// Службу гасят под обучение и забывают включить обратно. Пока простой короткий,
// стенд считается занятым: между эпохами обучения бывают паузы, и «сейчас
// свободно» ещё не значит «работа кончилась».
func TestGuardПогашеннаяСлужбаЖдётВыдержки(t *testing.T) {
	root := t.TempDir()
	cfg := полнаяПроверка()
	cfg.IdleBeforeStart = Duration(20 * time.Minute)
	g := &Guard{
		Client: fakeOllama(t, `{"models":[]}`), Cfg: cfg, Root: root,
		Run: fakeRun{gpuUsage: "300, 0\n", service: "inactive\n"}.run,
	}

	rep := g.Check(context.Background())
	if rep.Free || rep.NeedServiceStart {
		t.Fatalf("службу подняли, не выждав простоя: %+v", rep)
	}
	if !strings.Contains(strings.Join(rep.Blocking, " "), "из нужных") {
		t.Errorf("в причине не сказано, сколько ещё ждать: %v", rep.Blocking)
	}

	// Отматываем наблюдённый простой на полчаса назад — как будто карта
	// свободна уже давно и мы всё это время смотрели.
	backdateIdle(t, root, 30*time.Minute)

	rep = g.Check(context.Background())
	if !rep.Free || !rep.NeedServiceStart {
		t.Fatalf("после выдержки службу так и не решили поднять: %+v", rep)
	}
}

// Занятость сбрасывает счёт простоя: иначе пауза между эпохами обучения
// накопилась бы в «двадцать минут свободно» и отобрала карту.
func TestGuardЗанятостьСбрасываетПростой(t *testing.T) {
	root := t.TempDir()
	cfg := полнаяПроверка()
	cfg.IdleBeforeStart = Duration(20 * time.Minute)
	free := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: cfg, Root: root,
		Run: fakeRun{gpuUsage: "300, 0\n", service: "inactive\n"}.run}
	busy := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: cfg, Root: root,
		Run: fakeRun{gpuUsage: "66791, 87\n", service: "inactive\n"}.run}

	free.Check(context.Background())
	backdateIdle(t, root, 30*time.Minute)
	if rep := busy.Check(context.Background()); rep.IdleFor != 0 {
		t.Errorf("простой не обнулился при занятой карте: %s", rep.IdleFor)
	}
	rep := free.Check(context.Background())
	if rep.NeedServiceStart {
		t.Error("после занятости выдержку начали считать не с нуля")
	}
}

// Перерыв в наблюдении — не то же самое, что «было свободно»: вчерашняя
// отметка не имеет права выдать «свободна двадцать часов».
func TestGuardПерерывВНаблюденииСбрасываетПростой(t *testing.T) {
	root := t.TempDir()
	cfg := полнаяПроверка()
	cfg.IdleBeforeStart = Duration(20 * time.Minute)
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: cfg, Root: root,
		Run: fakeRun{gpuUsage: "300, 0\n", service: "inactive\n"}.run}

	g.Check(context.Background())
	// Отметка есть, но последний раз смотрели сутки назад.
	writeIdle(t, root, idleState{Since: time.Now().Add(-24 * time.Hour), Updated: time.Now().Add(-24 * time.Hour)})
	if rep := g.Check(context.Background()); rep.IdleFor > time.Minute {
		t.Errorf("простой засчитан за время, когда мы не смотрели: %s", rep.IdleFor)
	}
}

// Журнал проверок нужен ради утреннего вопроса «почему ночь не началась».
func TestGuardПишетЖурналПроверок(t *testing.T) {
	root := t.TempDir()
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: полнаяПроверка(), Root: root,
		Run: fakeRun{computeApps: "1644843, python, 66780 MiB\n", gpuUsage: "66791, 87\n", service: "inactive\n"}.run}
	g.Check(context.Background())

	b, err := os.ReadFile(filepath.Join(root, "logs", "guard.log"))
	if err != nil {
		t.Fatalf("журнал проверок не написан: %v", err)
	}
	line := string(b)
	for _, want := range []string{"занято", "66791", "87%", "inactive", "python"} {
		if !strings.Contains(line, want) {
			t.Errorf("в журнале нет %q: %s", want, line)
		}
	}
}

func writeIdle(t *testing.T, root string, st idleState) {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state", "gpu_idle.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func backdateIdle(t *testing.T, root string, d time.Duration) {
	t.Helper()
	writeIdle(t, root, idleState{Since: time.Now().Add(-d), Updated: time.Now()})
}
