package tools

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

func hangTestOptions(t *testing.T) (Options, string) {
	t.Helper()
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	sb, err := permissions.NewSandbox(realRoot, false, false, 512)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	return Options{Sandbox: sb, BashTimeout: 2 * time.Second, MaxOutputKB: 64}, realRoot
}

// TestBashReturnsDespiteSurvivingGrandchild — регрессия на реальное зависание.
//
// `dotnet run` запускает собранное приложение отдельным процессом. Прежняя
// реализация снимала по таймауту только сам dotnet, а внук продолжал держать
// канал вывода, из-за чего cmd.Wait() не возвращался никогда. Приложение
// зависало: Esc не помогал, выйти удавалось только закрытием терминала.
func TestBashReturnsDespiteSurvivingGrandchild(t *testing.T) {
	opts, _ := hangTestOptions(t)

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := runCommand(context.Background(),
			"sh -c 'sleep 60 & echo запущено; sleep 60'", opts, 2*time.Second)
		done <- err
	}()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err == nil {
			t.Error("прерванная по таймауту команда должна возвращать ошибку")
		}
		if !strings.Contains(err.Error(), "таймауту") {
			t.Errorf("ошибка должна объяснять причину: %v", err)
		}
		// 2 с таймаут + до 2 с на мягкое снятие + запас.
		if elapsed > 8*time.Second {
			t.Errorf("возврат занял %s — слишком долго", elapsed.Round(time.Millisecond))
		}
		t.Logf("вернулось за %s: %v", elapsed.Round(time.Millisecond), err)
	case <-time.After(15 * time.Second):
		t.Fatal("ЗАВИСАНИЕ: runCommand не вернулся — дефект воспроизвёлся снова")
	}
}

// TestBashKillsWholeProcessTree — снимать нужно всё дерево, а не только саму
// команду: иначе после прерывания на машине остаются работающие процессы.
func TestBashKillsWholeProcessTree(t *testing.T) {
	opts, root := hangTestOptions(t)
	pidFile := filepath.Join(root, "grandchild.pid")

	_, err := runCommand(context.Background(),
		"sh -c 'sleep 60 & echo $! > "+pidFile+"; sleep 60'", opts, 2*time.Second)
	if err == nil {
		t.Fatal("ожидалась ошибка таймаута")
	}

	data, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Skipf("потомок не успел записать свой pid: %v", rerr)
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(data)))
	if perr != nil {
		t.Skipf("не удалось прочитать pid: %v", perr)
	}

	// Даём мгновение на доставку сигнала.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // процесса больше нет — как и требовалось
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Подчистим за собой, чтобы тест не оставлял мусор.
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("порождённый процесс %d пережил прерывание команды", pid)
}

// TestBashCancelReturnsPromptly — отмена пользователем должна отпускать
// управление сразу, а не по истечении таймаута команды.
func TestBashCancelReturnsPromptly(t *testing.T) {
	opts, _ := hangTestOptions(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		_, _ = runCommand(ctx, "sh -c 'sleep 120 & sleep 120'", opts, 120*time.Second)
		done <- time.Since(start)
	}()

	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case elapsed := <-done:
		if elapsed > 6*time.Second {
			t.Errorf("после отмены возврат занял %s", elapsed.Round(time.Millisecond))
		}
		t.Logf("после отмены вернулось за %s", elapsed.Round(time.Millisecond))
	case <-time.After(15 * time.Second):
		t.Fatal("отмена не вернула управление")
	}
}

// TestBashNormalCommandStillWorks — починка не должна ломать обычный путь.
func TestBashNormalCommandStillWorks(t *testing.T) {
	opts, _ := hangTestOptions(t)

	out, err := runCommand(context.Background(), "echo привет", opts, 10*time.Second)
	if err != nil {
		t.Fatalf("простая команда: %v", err)
	}
	if !strings.Contains(out, "привет") || !strings.Contains(out, "Код возврата: 0") {
		t.Errorf("вывод команды: %q", out)
	}

	out, err = runCommand(context.Background(), "sh -c 'echo к stderr >&2; exit 3'", opts, 10*time.Second)
	if err != nil {
		t.Fatalf("команда с ненулевым кодом не должна давать ошибку Go: %v", err)
	}
	if !strings.Contains(out, "Код возврата: 3") || !strings.Contains(out, "к stderr") {
		t.Errorf("stderr и код возврата должны попадать в вывод: %q", out)
	}
}
