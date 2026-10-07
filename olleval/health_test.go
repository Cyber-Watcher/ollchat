package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

func psServer(t *testing.T, body string) *ollama.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return ollama.New(srv.URL, 5*time.Second, 5*time.Second, nil)
}

// Замерено на стенде: выезд 1.64 ГиБ за пределы карты уронил генерацию
// с 73.9 до 5.4 ток/с. Дальше мерить нечего — цифры уже не про модель.
func TestDoctorCatchesSpillToRAM(t *testing.T) {
	body := `{"models":[{"name":"nemotron:latest","size":84000000000,"size_vram":82000000000}]}`
	d := &Doctor{Client: psServer(t, body), Cfg: HealthCfg{CheckSpill: true}, Log: func(string, ...any) {}}
	spilled, details := d.Spilled(context.Background())
	if !spilled {
		t.Fatal("вытеснение не замечено")
	}
	if details == "" {
		t.Error("подробности не названы — в журнале ночи будет непонятно, что случилось")
	}
}

// Doctor не видит вытеснения когда всё на карте.
func TestDoctorSeesNoSpillWhenAllOnCard(t *testing.T) {
	body := `{"models":[{"name":"qwen3.5:122b","size":82500000000,"size_vram":82500000000}]}`
	d := &Doctor{Client: psServer(t, body), Cfg: HealthCfg{CheckSpill: true}, Log: func(string, ...any) {}}
	if spilled, _ := d.Spilled(context.Background()); spilled {
		t.Error("модель целиком на карте названа вытесненной")
	}
}

// Doctor проверку можно выключить.
func TestDoctorCheckCanBeDisabled(t *testing.T) {
	body := `{"models":[{"name":"x","size":84000000000,"size_vram":82000000000}]}`
	d := &Doctor{Client: psServer(t, body), Cfg: HealthCfg{CheckSpill: false}, Log: func(string, ...any) {}}
	if spilled, _ := d.Spilled(context.Background()); spilled {
		t.Error("проверка выключена, а вытеснение всё равно сообщается")
	}
}

// DoctorRestart ждёт подъёма.
func TestDoctorRestartWaitsForUp(t *testing.T) {
	var restarted bool
	d := &Doctor{
		Client: psServer(t, `{"version":"0.32.13"}`),
		Cfg:    HealthCfg{RestartWait: Duration(5 * time.Second)},
		Log:    func(string, ...any) {},
		Run: func(ctx context.Context, name string, args ...string) (string, int, error) {
			restarted = name == "sudo" && len(args) > 2 && args[2] == "ollama"
			return "", 0, nil
		},
	}
	if err := d.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if !restarted {
		t.Error("systemctl restart ollama не вызван")
	}
}

// Живая отметка означает «прогон идёт»; устаревшая — что он умер, и сервер
// после этого надо открывать, не дожидаясь конца окна.
func TestHeartbeat(t *testing.T) {
	root := t.TempDir()
	if _, alive := LiveRun(root, time.Minute); alive {
		t.Fatal("отметки нет, а прогон считается живым")
	}
	if err := WriteHeartbeat(root, Heartbeat{PID: os.Getpid(), Night: "2026-08-22", Model: "m", Task: "t"}); err != nil {
		t.Fatal(err)
	}
	hb, alive := LiveRun(root, time.Minute)
	if !alive || hb.Night != "2026-08-22" {
		t.Errorf("живая отметка не распознана: %+v %v", hb, alive)
	}
	if _, alive := LiveRun(root, time.Nanosecond); alive {
		t.Error("устаревшая отметка признана живой")
	}
	if err := ClearHeartbeat(root); err != nil {
		t.Fatal(err)
	}
	if _, alive := LiveRun(root, time.Minute); alive {
		t.Error("отметка снята, а прогон считается живым")
	}
}

// Файлы попытки и отметка жизни подменяются целиком: открытый прежде файл
// дочитывается прежним, а не половиной нового. Запись на месте сначала
// обрезала файл: оборванный metrics.json засчитывал попытку с битыми
// метриками, а отметка, пойманная между обрезкой и записью, читалась
// службой возврата как «прогон мёртв».
func TestRunFilesReplacedWhole(t *testing.T) {
	dir := t.TempDir()
	if err := WriteJSON(dir, "metrics.json", Metrics{Task: "old"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "metrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := WriteJSON(dir, "metrics.json", Metrics{Task: "new"}); err != nil {
		t.Fatal(err)
	}
	var m Metrics
	if err := json.NewDecoder(f).Decode(&m); err != nil || m.Task != "old" {
		t.Errorf("metrics.json переписан на месте: %q, %v", m.Task, err)
	}

	root := t.TempDir()
	if err := WriteHeartbeat(root, Heartbeat{Task: "old"}); err != nil {
		t.Fatal(err)
	}
	hf, err := os.Open(HeartbeatPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer hf.Close()
	if err := WriteHeartbeat(root, Heartbeat{Task: "new"}); err != nil {
		t.Fatal(err)
	}
	var hb Heartbeat
	if err := json.NewDecoder(hf).Decode(&hb); err != nil || hb.Task != "old" {
		t.Errorf("отметка переписана на месте: %q, %v", hb.Task, err)
	}
}

// Отметка от умершего процесса не должна удерживать сервер закрытым.
func TestHeartbeatDeadProcess(t *testing.T) {
	root := t.TempDir()
	if err := WriteHeartbeat(root, Heartbeat{PID: 999999, Night: "n"}); err != nil {
		t.Fatal(err)
	}
	if _, alive := LiveRun(root, time.Hour); alive {
		t.Error("отметка несуществующего процесса признана живой")
	}
}
