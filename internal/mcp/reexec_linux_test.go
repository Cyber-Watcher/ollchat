package mcp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stdio: бинарь подменили — цикл зовёт exec только между запросами, а если
// exec не удался, служит дальше прежним кодом (этап 109, А1).
func TestServeWatchedReexecOnReplace(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ollmcp")
	if err := os.WriteFile(bin, []byte("старый"), 0o755); err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(bin, &st); err != nil {
		t.Fatal(err)
	}
	w := &binaryWatch{path: bin, dev: uint64(st.Dev), ino: uint64(st.Ino)}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW := io.Pipe()
	execs := make(chan struct{}, 4)
	done := make(chan error, 1)
	go func() {
		done <- serveWatched(context.Background(), server(probe()), inR, outW, false, w, func() error {
			execs <- struct{}{}
			return errors.New("exec в тесте не делается")
		})
		outW.Close()
	}()
	replies := bufio.NewReader(outR)
	ask := func(id string) {
		t.Helper()
		if _, err := inW.WriteString(`{"jsonrpc":"2.0","id":` + id + `,"method":"tools/list"}` + "\n"); err != nil {
			t.Fatal(err)
		}
		line, err := replies.ReadString('\n')
		if err != nil || !strings.Contains(line, `"id":`+id) {
			t.Fatalf("ответ на %s: %q, %v", id, line, err)
		}
	}

	ask("1")
	if replaced := w.replaced(); replaced {
		t.Fatal("тот же файл принят за подменённый")
	}

	// Подмена так, как её делает выкладка: новый файл и mv поверх.
	fresh := bin + ".new"
	if err := os.WriteFile(fresh, []byte("новый"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(fresh, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fresh, bin); err != nil {
		t.Fatal(err)
	}
	select {
	case <-execs:
	case <-time.After(5 * time.Second):
		t.Fatal("подмену бинаря не заметили за 5 с")
	}

	// exec не удался — служба отвечает, и второй попытки нет.
	ask("2")
	inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("цикл вернул ошибку: %v", err)
	}
	if n := len(execs); n != 0 {
		t.Errorf("после неудачного exec ещё %d попыток", n)
	}
}

// Правка файла настроек перезапускает службу так же, как подмена бинаря,
// а негодная правка — нет: служба остаётся на прежних настройках.
func TestSettingsChangeTriggersReexec(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "ollmcp.toml")
	if err := os.WriteFile(conf, []byte("output_kb = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := true
	w := &binaryWatch{path: os.Args[0], files: []fileSig{sigOf(conf)},
		validate: func() error {
			if valid {
				return nil
			}
			return errors.New("негодно")
		}}
	// Бинарь теста по своему пути не менялся: dev/ino возьмём настоящие.
	var st syscall.Stat_t
	if err := syscall.Stat(os.Args[0], &st); err != nil {
		t.Fatal(err)
	}
	w.dev, w.ino = uint64(st.Dev), uint64(st.Ino)
	if w.replaced() {
		t.Fatal("без правок принято за перемену")
	}

	edit := func(body string) {
		t.Helper()
		if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Minute)
		if err := os.Chtimes(conf, old, old); err != nil {
			t.Fatal(err)
		}
	}
	valid = false
	edit("output_kb = 22\n")
	if w.replaced() {
		t.Error("негодная правка вызвала перезапуск")
	}
	if w.replaced() {
		t.Error("та же негодная правка проверяется повторно на каждом круге")
	}
	valid = true
	edit("output_kb = 333\n")
	if !w.replaced() {
		t.Error("годная правка не замечена")
	}
	if err := os.Remove(conf); err != nil {
		t.Fatal(err)
	}
	if !w.replaced() {
		t.Error("удаление файла настроек не замечено (умолчания — тоже перемена)")
	}
}
