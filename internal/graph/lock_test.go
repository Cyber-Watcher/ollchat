package graph

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newLockedGraph — пустой граф во временном каталоге.
func newLockedGraph(t *testing.T) *Graph {
	t.Helper()
	g, err := Create(t.TempDir(), "проба", 10, Rules{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

// Признак от живого процесса не трогается, а отказ называет номер процесса
// и путь к файлу: без них «сборка уже идёт» — тупик.
func TestLockRefusesWhileOwnerAlive(t *testing.T) {
	g := newLockedGraph(t)
	if err := g.Lock(); err != nil {
		t.Fatalf("первый Lock: %v", err)
	}
	defer g.Unlock()

	g2, err := Open(g.Dir()[:len(g.Dir())-len("/"+DirName)], 10, Rules{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer g2.Close()

	err = g2.Lock()
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("ожидался ErrLocked, получено %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, fmt.Sprint(os.Getpid())) {
		t.Errorf("в отказе нет номера процесса: %q", msg)
	}
	if !strings.Contains(msg, lockFile) {
		t.Errorf("в отказе нет пути к признаку: %q", msg)
	}
	if g2.StaleLock() != "" {
		t.Errorf("живой признак не должен считаться брошенным: %q", g2.StaleLock())
	}
}

// Признак от неживого процесса снимается сам — иначе после kill -9 докатка
// упирается в файл, и человек без подсказки не знает, что делать.
func TestLockTakesOverStaleLock(t *testing.T) {
	g := newLockedGraph(t)

	// Номер процесса, которого заведомо нет: занимаем и сразу освобождаем.
	dead := findDeadPID(t)
	path := filepath.Join(g.Dir(), lockFile)
	body := fmt.Sprintf("pid %d, начато %s\n", dead, time.Now().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("подготовка признака: %v", err)
	}

	if err := g.Lock(); err != nil {
		t.Fatalf("брошенный признак должен сниматься сам, получено: %v", err)
	}
	defer g.Unlock()

	if s := g.StaleLock(); !strings.Contains(s, fmt.Sprint(dead)) {
		t.Errorf("о снятом признаке не сказано внятно: %q", s)
	}
	// Признак теперь наш: внутри наш номер процесса.
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), fmt.Sprint(os.Getpid())) {
		t.Errorf("признак не перезаписан на себя: %q", b)
	}
}

// Испорченный признак трогать нельзя: помешать чужой работе хуже, чем
// попросить человека разобраться самому.
func TestLockKeepsUnreadableLock(t *testing.T) {
	g := newLockedGraph(t)
	path := filepath.Join(g.Dir(), lockFile)
	if err := os.WriteFile(path, []byte("что-то не то\n"), 0o644); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	if err := g.Lock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("ожидался отказ, получено %v", err)
	}
}

// findDeadPID возвращает номер процесса, которого точно нет.
func findDeadPID(t *testing.T) int {
	t.Helper()
	for pid := 1 << 21; pid > 1<<20; pid-- {
		if p, err := os.FindProcess(pid); err == nil {
			if err := p.Signal(nil); err != nil {
				return pid
			}
		}
	}
	t.Skip("не нашлось заведомо мёртвого номера процесса")
	return 0
}
