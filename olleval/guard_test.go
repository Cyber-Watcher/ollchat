package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Guard свободная карта.
func TestGuardFreeCard(t *testing.T) {
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: testGuardCfg(15 * time.Minute),
		Run: (&fakeRun{gpu: "\n", journal: "-- No entries --\n"}).run}
	rep := g.Check(context.Background())
	if !rep.Free {
		t.Errorf("карта названа занятой: %v", rep.Blocking)
	}
}

// Guard занятая карта.
func TestGuardBusyCard(t *testing.T) {
	cases := map[string]struct {
		ps  string
		smi string
		log string
	}{
		"процесс на карте": {`{"models":[]}`, "492677, python, 25138\n", ""},
		"модель в памяти":  {`{"models":[{"name":"qwen3.5:122b","size_vram":80000000000}]}`, "", ""},
		"свежие запросы":   {`{"models":[]}`, "", "POST /api/chat 200\nPOST /api/chat 200\n"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			// Карта не разобрана — замера загрузки нет, правила строгие.
			g := &Guard{Client: fakeOllama(t, c.ps), Cfg: testGuardCfg(15 * time.Minute),
				Run: (&fakeRun{computeApps: c.smi, journal: c.log}).run}
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
func TestGuardForeignSessionIsInfoOnly(t *testing.T) {
	t.Setenv("USER", "olleval")
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: testGuardCfg(time.Minute),
		Run: (&fakeRun{sessions: "user1 pts/0    198.51.100.10        Thu03   32:18   0.14s   ?   tmux\n"}).run}
	rep := g.Check(context.Background())
	if !rep.Free {
		t.Errorf("чужой сеанс запретил прогон: %v", rep.Blocking)
	}
	if len(rep.Notes) == 0 || !strings.Contains(strings.Join(rep.Notes, " "), "user1") {
		t.Errorf("чужой сеанс не отмечен: %v", rep.Notes)
	}
}

// Старт только после нескольких свободных проверок подряд: человек может
// сесть за стенд в полночь и проработать десять минут, а одна удачная проверка
// в паузе между его запросами отобрала бы у него память.
func TestWaitFreeNeedsConsecutiveFree(t *testing.T) {
	states := []string{"занято", "свободно", "занято", "свободно", "свободно", "свободно"}
	var i int
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: testGuardCfg(time.Minute),
		Run: func(ctx context.Context, cmd string, args ...string) (string, int, error) {
			// Состояние сменяется по одному вопросу за проверку — по списку
			// процессов; другие вопросы к nvidia-smi (карта и загрузка) идут
			// в той же проверке и счётчик двигать не должны. Карт nvidia-smi
			// не называет — замера загрузки нет, и процесс на карте запрещает.
			if cmd != "nvidia-smi" || len(args) == 0 || !strings.Contains(args[0], "compute-apps") {
				return "", 0, nil
			}
			state := states[min(i, len(states)-1)]
			i++
			if state == "занято" {
				return "1234, python, 25138\n", 0, nil
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
func TestWaitFreeGivesUpOnTimeout(t *testing.T) {
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: testGuardCfg(time.Minute),
		Run: (&fakeRun{computeApps: "1234, python, 25138\n"}).run}
	_, ok := g.WaitFree(context.Background(), 3, time.Millisecond, time.Now().Add(5*time.Millisecond), func(string) {})
	if ok {
		t.Error("ожидание закончилось стартом, хотя карта всё время занята")
	}
}

// Без ожидания guard обязан ответить сразу: проверить и сказать. Иначе
// команда без флагов молча висит на занятой карте.
func TestWaitFreeAnswersAtOnceWithoutWait(t *testing.T) {
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: testGuardCfg(time.Minute),
		Run: (&fakeRun{computeApps: "1234, python, 25138\n"}).run}
	began := time.Now()
	_, ok := g.WaitFree(context.Background(), 3, time.Minute, time.Now(), func(string) {})
	if ok {
		t.Error("занятая карта названа свободной")
	}
	if time.Since(began) > 5*time.Second {
		t.Errorf("проверка заняла %s — команда без ожидания не должна ждать", time.Since(began))
	}
}

// card — строка карты в ответе nvidia-smi на главный запрос nodeprobe
// (индекс, имя, всего, занято, свободно, загрузка, температура).
func card(usedMiB, util int) string {
	return fmt.Sprintf("0, NVIDIA A100-SXM4-80GB, 81920, %d, %d, %d, 41\n", usedMiB, 81920-usedMiB, util)
}

// sample — строка повторной выборки загрузки (индекс, загрузка, занято).
func sample(usedMiB, util int) string {
	return fmt.Sprintf("0, %d, %d\n", util, usedMiB)
}

// fakeRun изображает внешние команды так, как их зовёт nodeprobe: ключ —
// имя программы и первый аргумент. Карта задаётся строкой card(), процессы —
// в виде «pid, имя, МиБ», служба — состоянием, журнал и сеансы — выводом.
// Пустая карта означает «nvidia-smi ответил, но карт не назвал»: замера
// загрузки нет, и guard переходит на строгие правила.
type fakeRun struct {
	gpu         string   // ответ на главный запрос --query-gpu (см. card)
	utilSeq     []string // повторные выборки загрузки (см. sample), по одной за вызов
	computeApps string   // ответ на --query-compute-apps: "pid, имя, МиБ"
	gpuErr      error    // nvidia-smi отсутствует
	service     string   // состояние службы: active, inactive, …
	journal     string
	sessions    string // вывод w -h
}

func (f *fakeRun) run(ctx context.Context, name string, args ...string) (string, int, error) {
	key := name
	if len(args) > 0 {
		key += " " + args[0]
	}
	switch {
	case strings.HasPrefix(key, "nvidia-smi --query-gpu=index,name"):
		if f.gpuErr != nil {
			return "", 1, f.gpuErr
		}
		return f.gpu, 0, nil
	case strings.HasPrefix(key, "nvidia-smi --query-gpu=index,utilization"):
		if len(f.utilSeq) == 0 {
			return "", 1, errors.New("выборка не задана в тесте")
		}
		out := f.utilSeq[0]
		f.utilSeq = f.utilSeq[1:]
		return out, 0, nil
	case strings.HasPrefix(key, "nvidia-smi --query-gpu=index,power"):
		return "0, 71.35, Not Active\n", 0, nil
	case strings.HasPrefix(key, "nvidia-smi --query-compute-apps"):
		return f.computeApps, 0, nil
	case key == "systemctl show":
		return "ActiveState=" + strings.TrimSpace(f.service) + "\nMainPID=0\n", 0, nil
	case key == "sudo journalctl":
		return f.journal, 0, nil
	case key == "w -h":
		return f.sessions, 0, nil
	}
	return "", 1, errors.New("команда не задана в тесте: " + key)
}

func fullCheck() GuardCfg {
	c := testGuardCfg(15 * time.Minute)
	c.CheckService = true
	c.Service = "ollama"
	c.BusyVRAMMiB = 1024
	c.BusyUtilPercent = 10
	c.LogChecks = true
	return c
}

// Обучение модели питоновским скриптом: Ollama на это время гасят, чтобы
// освободить видеопамять. Прогон в такой момент поднял бы службу за спиной
// у человека и отобрал карту посреди обучения.
func TestGuardTrainingOnCardWithOllamaStopped(t *testing.T) {
	g := &Guard{
		Client: fakeOllama(t, `{"models":[]}`),
		Cfg:    fullCheck(),
		Run: (&fakeRun{
			computeApps: "1644843, python, 66780\n",
			gpu:         card(66791, 87),
			service:     "inactive",
		}).run,
	}
	rep := g.Check(context.Background())
	if rep.Free {
		t.Fatal("карта названа свободной во время обучения")
	}
	all := strings.Join(rep.Blocking, " | ")
	for _, want := range []string{"загружена на 87%", "служба ollama не запущена"} {
		if !strings.Contains(all, want) {
			t.Errorf("в причинах нет %q: %s", want, all)
		}
	}
	// Процесс на карте сам по себе прогон не запрещает, но в отчёте виден:
	// по нему утром понятно, кто держал стенд.
	if notes := strings.Join(rep.Notes, " | "); !strings.Contains(notes, "python") {
		t.Errorf("процесс на карте не отмечен: %s", notes)
	}
}

// Занятая память при простаивающей карте прогон не запрещает: модель остаётся
// в видеопамяти часами после чужого запроса, и ждать пустой карты значит
// не начать ночь никогда.
func TestGuardUsedMemoryWhileIdleDoesNotBlock(t *testing.T) {
	g := &Guard{
		Client: fakeOllama(t, `{"models":[]}`),
		Cfg:    fullCheck(),
		Run:    (&fakeRun{computeApps: "", gpu: card(40000, 5), service: "active"}).run,
	}
	rep := g.Check(context.Background())
	if !rep.Free {
		t.Fatalf("40 ГиБ памяти при загрузке 5%% остановили прогон: %v", rep.Blocking)
	}
	if rep.UtilLimit != 10 {
		t.Errorf("порог в отчёте = %d%%, ожидался общий 10%%", rep.UtilLimit)
	}
}

// Забытая в памяти модель — не работа: карта при ней не считает. Прогон
// начинается, а модель снимается с карты перед первым замером.
func TestGuardForgottenModelDoesNotBlock(t *testing.T) {
	g := &Guard{
		Client: fakeOllama(t, `{"models":[{"name":"qwen3.5:122b","size_vram":80000000000}]}`),
		Cfg:    fullCheck(),
		Run:    (&fakeRun{gpu: card(76000, 0), service: "active"}).run,
	}
	rep := g.Check(context.Background())
	if !rep.Free {
		t.Fatalf("модель в памяти при нулевой загрузке остановила прогон: %v", rep.Blocking)
	}
	if notes := strings.Join(rep.Notes, " | "); !strings.Contains(notes, "выгружу перед прогоном") {
		t.Errorf("о забытой модели не сказано: %s", notes)
	}
}

// Та же модель, но по ней идёт чужой ответ: карта считает — ждём.
func TestGuardModelUnderLoadBlocks(t *testing.T) {
	g := &Guard{
		Client: fakeOllama(t, `{"models":[{"name":"qwen3.5:122b","size_vram":80000000000}]}`),
		Cfg:    fullCheck(),
		Run:    (&fakeRun{gpu: card(76000, 45), service: "active"}).run,
	}
	rep := g.Check(context.Background())
	if rep.Free {
		t.Fatal("работающая модель названа простоем")
	}
	if all := strings.Join(rep.Blocking, " | "); !strings.Contains(all, "загружена на 45%") {
		t.Errorf("причина названа неверно: %s", all)
	}
}

// Порог берётся из промежутка расписания: в одном окне пять процентов
// допустимы, в другом требуется строгий ноль.
func TestGuardThresholdFromSchedule(t *testing.T) {
	cases := map[int]bool{0: false, 5: true, 10: true}
	for limit, wantFree := range cases {
		g := &Guard{
			Client: fakeOllama(t, `{"models":[]}`),
			Cfg:    fullCheck(),
			Run:    (&fakeRun{gpu: card(70000, 4), service: "active"}).run,
			Limit:  func(time.Time) int { return limit },
		}
		rep := g.Check(context.Background())
		if rep.Free != wantFree {
			t.Errorf("порог %d%% при загрузке 4%%: свободна = %v, ожидалось %v (%v)",
				limit, rep.Free, wantFree, rep.Blocking)
		}
	}
}

// В расчёт идёт наибольшая выборка серии: единственный снимок nvidia-smi
// попадает в паузу между двумя токенами чужого ответа и врёт про простой.
func TestGuardTakesLargestSample(t *testing.T) {
	cfg := fullCheck()
	cfg.UtilSamples = 5
	cfg.UtilSampleGap = Duration(time.Millisecond)
	// Первая выборка приходит с главным запросом о карте, остальные четыре —
	// повторными запросами загрузки.
	g := &Guard{
		Client: fakeOllama(t, `{"models":[]}`),
		Cfg:    cfg,
		Run: (&fakeRun{
			gpu:     card(70000, 0),
			utilSeq: []string{sample(70000, 0), sample(70000, 38), sample(70000, 0), sample(70000, 1)},
			service: "active",
		}).run,
	}
	rep := g.Check(context.Background())
	if rep.Free {
		t.Fatal("всплеск 38% посреди серии пропущен")
	}
	if rep.GPUUtil != 38 || len(rep.GPUSamples) != 5 {
		t.Errorf("выборки разобраны неверно: max %d%%, серия %v", rep.GPUUtil, rep.GPUSamples)
	}
}

// Если загрузку измерить не удалось, работает прежнее строгое правило:
// слепой старт хуже пропущенной ночи.
func TestGuardStrictWithoutMeasurement(t *testing.T) {
	g := &Guard{
		Client: fakeOllama(t, `{"models":[{"name":"qwen3.5:122b","size_vram":80000000000}]}`),
		Cfg:    fullCheck(),
		Run:    (&fakeRun{computeApps: "1644843, python, 66780\n", gpu: "нет ответа\n", service: "active"}).run,
	}
	rep := g.Check(context.Background())
	if rep.Free {
		t.Fatal("карта названа свободной без замера загрузки")
	}
	all := strings.Join(rep.Blocking, " | ")
	for _, want := range []string{"python", "загружена модель"} {
		if !strings.Contains(all, want) {
			t.Errorf("в причинах нет %q: %s", want, all)
		}
	}
}

// Загрузка без заметной памяти — тоже занятость: кто-то считает на карте.
func TestGuardLoadedCardWithoutMemory(t *testing.T) {
	g := &Guard{
		Client: fakeOllama(t, `{"models":[]}`),
		Cfg:    fullCheck(),
		Run:    (&fakeRun{gpu: card(200, 75), service: "active"}).run,
	}
	if rep := g.Check(context.Background()); rep.Free {
		t.Error("75% загрузки не остановили прогон")
	}
}

// Пустая карта с работающей службой — можно начинать. Немного занятой памяти
// (драйвер, буферы) порогом не считается, иначе прогон не начался бы никогда.
func TestGuardIdleCardWithRunningService(t *testing.T) {
	g := &Guard{
		Client: fakeOllama(t, `{"models":[]}`),
		Cfg:    fullCheck(),
		Run:    (&fakeRun{gpu: card(300, 0), service: "active"}).run,
	}
	rep := g.Check(context.Background())
	if !rep.Free {
		t.Errorf("свободная карта названа занятой: %v", rep.Blocking)
	}
}

// Без nvidia-smi замера нет и процессы не видны: остаются модель в памяти
// и порог занятой памяти, а причина попадает в заметки.
func TestGuardWithoutNvidiaSmiIsStrict(t *testing.T) {
	g := &Guard{
		Client: fakeOllama(t, `{"models":[{"name":"qwen3.5:122b","size_vram":80000000000}]}`),
		Cfg:    fullCheck(),
		Run:    (&fakeRun{gpuErr: errors.New("exec: nvidia-smi not found"), service: "active"}).run,
	}
	rep := g.Check(context.Background())
	if rep.Free {
		t.Fatal("карта названа свободной без nvidia-smi")
	}
	if all := strings.Join(rep.Blocking, " | "); !strings.Contains(all, "загружена модель") {
		t.Errorf("в причинах нет модели: %s", all)
	}
	if notes := strings.Join(rep.Notes, " | "); !strings.Contains(notes, "nvidia-smi") {
		t.Errorf("отсутствие nvidia-smi не отмечено: %s", notes)
	}
}

// Службу гасят под обучение и забывают включить обратно. Пока простой короткий,
// стенд считается занятым: между эпохами обучения бывают паузы, и «сейчас
// свободно» ещё не значит «работа кончилась».
func TestGuardStoppedServiceWaitsHoldoff(t *testing.T) {
	root := t.TempDir()
	cfg := fullCheck()
	cfg.IdleBeforeStart = Duration(20 * time.Minute)
	g := &Guard{
		Client: fakeOllama(t, `{"models":[]}`), Cfg: cfg, Root: root,
		Run: (&fakeRun{gpu: card(300, 0), service: "inactive"}).run,
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
func TestGuardBusyResetsIdle(t *testing.T) {
	root := t.TempDir()
	cfg := fullCheck()
	cfg.IdleBeforeStart = Duration(20 * time.Minute)
	free := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: cfg, Root: root,
		Run: (&fakeRun{gpu: card(300, 0), service: "inactive"}).run}
	busy := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: cfg, Root: root,
		Run: (&fakeRun{gpu: card(66791, 87), service: "inactive"}).run}

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
func TestGuardWatchGapResetsIdle(t *testing.T) {
	root := t.TempDir()
	cfg := fullCheck()
	cfg.IdleBeforeStart = Duration(20 * time.Minute)
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: cfg, Root: root,
		Run: (&fakeRun{gpu: card(300, 0), service: "inactive"}).run}

	g.Check(context.Background())
	// Отметка есть, но последний раз смотрели сутки назад.
	writeIdle(t, root, idleState{Since: time.Now().Add(-24 * time.Hour), Updated: time.Now().Add(-24 * time.Hour)})
	if rep := g.Check(context.Background()); rep.IdleFor > time.Minute {
		t.Errorf("простой засчитан за время, когда мы не смотрели: %s", rep.IdleFor)
	}
}

// Журнал проверок нужен ради утреннего вопроса «почему ночь не началась».
func TestGuardWritesCheckLog(t *testing.T) {
	root := t.TempDir()
	g := &Guard{Client: fakeOllama(t, `{"models":[]}`), Cfg: fullCheck(), Root: root,
		Run: (&fakeRun{computeApps: "1644843, python, 66780\n", gpu: card(66791, 87), service: "inactive"}).run}
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
