package graph

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Брошенный признак сборки разом находят несколько претендентов — замок
// достаётся ровно одному.
//
// До 07.10.2026 снятие брошенного признака шло «прочитать — убедиться в
// смерти хозяина — убрать — создать свой», и второй претендент, прочитавший
// тот же брошенный признак, убирал уже свежий признак первого: замок
// оказывался у обоих, и две сборки писали в одни журналы (аудит, 4.5).
func TestStaleLockTakenOverByOne(t *testing.T) {
	dead := findDeadPID(t)
	for round := 0; round < 100; round++ {
		dir := t.TempDir()
		stale := fmt.Sprintf("pid %d, начато %s\n", dead, time.Now().Format(time.RFC3339))
		must(t, os.WriteFile(filepath.Join(dir, lockFile), []byte(stale), 0o644))

		const takers = 8
		var wg sync.WaitGroup
		var won atomic.Int32
		start := make(chan struct{})
		for i := 0; i < takers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if f, _, err := takeLock(dir); err == nil {
					won.Add(1)
					f.Close() // признак остаётся на диске: остальные обязаны его увидеть
				}
			}()
		}
		close(start)
		wg.Wait()
		if n := won.Load(); n != 1 {
			t.Fatalf("круг %d: брошенный признак заняли %d претендентов", round, n)
		}
	}
}

// startSleeper — живой процесс с чужим именем: им притворяется сборка,
// запущенная переименованным бинарём.
func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("не запустить подставной процесс: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// Живой признак переименованного бинаря не снимается: живость сверяется
// по времени старта процесса, записанному в признак, а не по имени программы
// в /proc/PID/cmdline. Чужой процесс, получивший тот же номер, живым
// не считается.
func TestLockOwnerAliveByStartTime(t *testing.T) {
	if _, ok := procStart(os.Getpid()); !ok {
		t.Skip("нет /proc: время старта процесса узнать неоткуда")
	}
	cmd := startSleeper(t)
	pid := cmd.Process.Pid
	start, ok := procStart(pid)
	if !ok {
		t.Fatal("время старта живого процесса не прочиталось")
	}
	path := filepath.Join(t.TempDir(), lockFile)

	write := func(body string) lockOwner {
		t.Helper()
		must(t, os.WriteFile(path, []byte(body), 0o644))
		return readLock(path)
	}
	since := time.Now().Format(time.RFC3339)
	if o := write(fmt.Sprintf("pid %d, начато %s\nстарт %s\n", pid, since, start)); !o.alive() {
		t.Fatal("живой хозяин с чужим именем программы признан мёртвым")
	}
	if o := write(fmt.Sprintf("pid %d, начато %s\nстарт %s1\n", pid, since, start)); o.alive() {
		t.Fatal("процесс с тем же номером, но другим временем старта признан хозяином")
	}
	// Признак прежней записи (без времени старта) разбирается по-прежнему.
	if o := write(fmt.Sprintf("pid %d, начато %s\n", pid, since)); o.PID != pid || o.Since != since {
		t.Fatalf("прежний признак разобран не так: %+v", o)
	}
}

// Признак новой записи читается и прежним разбором: первая строка та же,
// время старта — отдельной строкой ниже.
func TestLockBodyReadableByOldParser(t *testing.T) {
	var pid int
	var since string
	if _, err := fmt.Sscanf(lockBody(), "pid %d, начато %s", &pid, &since); err != nil || pid != os.Getpid() {
		t.Fatalf("прежний разбор не понял новый признак: pid=%d, %v", pid, err)
	}
	if _, err := time.Parse(time.RFC3339, since); err != nil {
		t.Fatalf("время начала испорчено для прежнего разбора: %q", since)
	}
}
