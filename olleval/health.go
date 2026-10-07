package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/fsx"
	"github.com/Cyber-Watcher/ollchat/internal/ollama"
)

// Doctor следит за здоровьем прогона: не выехала ли модель в оперативную
// память и не завис ли сервер.
//
// Обе беды тихие. Выезд в оперативную память замерен на этом стенде: 1.64 ГиБ
// за пределами карты уронили генерацию с 73.9 до 5.4 ток/с — в 13.7 раза.
// Прогон при этом продолжается как ни в чём не бывало, и вся ночь оказывается
// замером не умений модели, а скорости шины. Зависание выглядит ещё проще:
// задачи одна за другой упираются в таймаут, а сервер уже не отвечает.
type Doctor struct {
	Client *ollama.Client
	Cfg    HealthCfg
	Run    func(ctx context.Context, name string, args ...string) (string, int, error)
	Log    func(format string, args ...any)
}

// NewDoctor готовит слежение по настройкам.
func NewDoctor(c *ollama.Client, cfg HealthCfg, log func(string, ...any)) *Doctor {
	return &Doctor{Client: c, Cfg: cfg, Run: runCommand, Log: log}
}

// Spilled сообщает, выехала ли загруженная модель за пределы видеопамяти.
// Признак прямой: в /api/ps размер модели больше того, что лежит на карте.
func (d *Doctor) Spilled(ctx context.Context) (bool, string) {
	if !d.Cfg.CheckSpill {
		return false, ""
	}
	running, err := d.Client.PS(ctx)
	if err != nil {
		return false, ""
	}
	for _, m := range running {
		if m.SizeVRAM > 0 && m.Size > m.SizeVRAM {
			out := float64(m.Size-m.SizeVRAM) / (1 << 30)
			return true, fmt.Sprintf("%s: на карте %.1f ГиБ из %.1f, в оперативной памяти %.2f ГиБ",
				m.Name, float64(m.SizeVRAM)/(1<<30), float64(m.Size)/(1<<30), out)
		}
	}
	return false, ""
}

// Healthy — отвечает ли сервер вообще.
func (d *Doctor) Healthy(ctx context.Context) bool {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := d.Client.Version(reqCtx)
	return err == nil
}

// Restart перезапускает Ollama и ждёт, пока она поднимется.
//
// Перезапуск освобождает карту целиком: это лечит и зависшую генерацию,
// и застрявшую в памяти модель. Прогон после этого продолжается с того места,
// где остановился, — сделанные попытки видны по их metrics.json и заново
// не гоняются.
func (d *Doctor) Restart(ctx context.Context) error {
	d.Log("перезапускаю Ollama")
	if err := d.systemctl(ctx, "restart", "ollama"); err != nil {
		return fmt.Errorf("перезапуск Ollama: %w", err)
	}
	wait := d.Cfg.RestartWait.Get(5 * time.Minute)
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if d.Healthy(ctx) {
			d.Log("Ollama поднялась")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("Ollama не поднялась за %s", wait)
}

// StartService поднимает погашенную службу и ждёт, пока она ответит.
//
// Отдельно от Restart: перезапуск лечит зависшую службу, а это — включение
// забытой. Разница важна в журнале: «перезапустил, потому что не отвечала»
// и «поднял, потому что её забыли включить после обучения» — разные события.
func (d *Doctor) StartService(ctx context.Context, name string) error {
	if name == "" {
		name = "ollama"
	}
	d.Log("поднимаю службу %s", name)
	if err := d.systemctl(ctx, "start", name); err != nil {
		return fmt.Errorf("запуск службы %s: %w", name, err)
	}
	wait := d.Cfg.RestartWait.Get(5 * time.Minute)
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if d.Healthy(ctx) {
			d.Log("служба %s поднята", name)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("служба %s не поднялась за %s", name, wait)
}

// systemctl зовёт `sudo -n systemctl <verb> <unit>` и считает сбоем
// и ненулевой код.
//
// runCommand отдаёт код возврата как результат, а не как ошибку: так нужно
// docker и nvidia-smi. Но здесь это значило, что отказ sudo (нет NOPASSWD,
// не та служба) выглядел успехом: прогон писал «перезапускаю Ollama», ждал
// её — живую и нетронутую — и считал сервер вылеченным, а погашенную службу
// «поднимал» пять минут впустую. -n — потому что прогон идёт в tmux, где
// у sudo есть терминал: без -n он ждал бы пароля, которого ночью никто
// не введёт.
func (d *Doctor) systemctl(ctx context.Context, verb, unit string) error {
	out, code, err := d.Run(ctx, "sudo", "-n", "systemctl", verb, unit)
	if err == nil && code != 0 {
		err = fmt.Errorf("код %d: %s", code, strings.TrimSpace(tail(out, 500)))
	}
	if err != nil {
		return fmt.Errorf("sudo systemctl %s %s: %w", verb, unit, err)
	}
	return nil
}

// ── Отметка о живом прогоне ──────────────────────────────────────────────────

// Heartbeat — отметка «прогон идёт», которую видит служба возврата сервера.
//
// Без неё не обойтись: в выходное окно на целые сутки возврат по расписанию
// открыл бы сервер посреди работающего прогона, а умерший посреди ночи прогон,
// наоборот, оставил бы стенд закрытым до конца окна.
type Heartbeat struct {
	PID     int       `json:"pid"`
	Night   string    `json:"night"`
	Model   string    `json:"model"`
	Task    string    `json:"task"`
	Started time.Time `json:"started"`
	Updated time.Time `json:"updated"`
}

// HeartbeatPath — где лежит отметка.
func HeartbeatPath(root string) string { return filepath.Join(root, "state", "run.lock") }

// WriteHeartbeat обновляет отметку.
//
// Атомарно: служба возврата читает отметку в любой момент, и файл,
// пойманный между обрезкой и записью, не разбирался — LiveRun считал
// прогон мёртвым, и сервер открывали людям посреди замера.
func WriteHeartbeat(root string, hb Heartbeat) error {
	hb.Updated = time.Now()
	if err := os.MkdirAll(filepath.Dir(HeartbeatPath(root)), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(HeartbeatPath(root), append(b, '\n'), 0o644)
}

// ClearHeartbeat снимает отметку по окончании прогона.
func ClearHeartbeat(root string) error {
	err := os.Remove(HeartbeatPath(root))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// LiveRun возвращает отметку, если прогон действительно жив: процесс на месте
// и отметка свежая. Устаревшая отметка — признак умершего прогона, и сервер
// после такого надо открывать, а не ждать конца окна.
func LiveRun(root string, stale time.Duration) (Heartbeat, bool) {
	b, err := os.ReadFile(HeartbeatPath(root))
	if err != nil {
		return Heartbeat{}, false
	}
	var hb Heartbeat
	if err := json.Unmarshal(b, &hb); err != nil {
		return Heartbeat{}, false
	}
	if time.Since(hb.Updated) > stale {
		return hb, false
	}
	if hb.PID > 0 && !processAlive(hb.PID) {
		return hb, false
	}
	return hb, true
}

// processAlive проверяет, жив ли процесс: сигнал 0 ничего не делает, но
// сообщает, есть ли кому его получить.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
