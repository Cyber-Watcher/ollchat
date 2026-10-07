package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/permissions"
)

// Строка запуска собирается ровно так, как задумано: система только для
// чтения, свой /dev, /proc и /tmp, корень на запись, спрятанное — поверх.
func TestBwrapArgsLayout(t *testing.T) {
	hide := []hiddenPath{
		{path: "/home/u/.ssh", dir: true},
		{path: "/home/u/.netrc", dir: false},
	}
	got := bwrapArgs(true, "/work/proj", hide, []string{"/usr/bin/ls", "-la"})
	want := []string{
		"--die-with-parent", "--unshare-all", "--share-net",
		"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp",
		"--bind", "/work/proj", "/work/proj",
		"--tmpfs", "/home/u/.ssh",
		"--ro-bind", "/dev/null", "/home/u/.netrc",
		"--chdir", "/work/proj", "--", "/usr/bin/ls", "-la",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("строка запуска:\n  %q\nожидалось\n  %q", got, want)
	}
}

// Без сети --share-net нет; спрятанный путь, внутри которого лежит сам
// корень, монтируется раньше корня — иначе он закрыл бы и корень.
func TestBwrapArgsNoNetworkAndHiddenAroundRoot(t *testing.T) {
	hide := []hiddenPath{
		{path: "/home/u/.config/ollchat", dir: true},
		{path: "/home/u/.config/ollchat/work/.secret", dir: true},
	}
	got := strings.Join(bwrapArgs(false, "/home/u/.config/ollchat/work", hide, []string{"/bin/sh", "-c", "ls"}), " ")
	if strings.Contains(got, "--share-net") {
		t.Errorf("без сети остался --share-net: %s", got)
	}
	around := strings.Index(got, "--tmpfs /home/u/.config/ollchat ")
	bind := strings.Index(got, "--bind /home/u/.config/ollchat/work ")
	inside := strings.Index(got, "--tmpfs /home/u/.config/ollchat/work/.secret")
	if around < 0 || bind < 0 || inside < 0 || !(around < bind && bind < inside) {
		t.Errorf("порядок монтирования нарушен: %s", got)
	}
	if !strings.HasSuffix(got, "-- /bin/sh -c ls") {
		t.Errorf("команда не в конце: %s", got)
	}
}

// Прячется только то, что есть, — и по настоящему месту, а не по ссылке.
func TestHiddenPathsResolvesAndSkips(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "keys")
	file := filepath.Join(base, "token")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("ссылки недоступны: %v", err)
	}
	realDir, _ := filepath.EvalSymlinks(dir)
	realFile, _ := filepath.EvalSymlinks(file)

	got := hiddenPaths([]string{dir, file, filepath.Join(base, "нет"), link})
	want := []hiddenPath{{realDir, true}, {realFile, false}, {realDir, true}}
	if len(got) != len(want) {
		t.Fatalf("получено %+v, ожидалось %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, ожидалось %+v", i, got[i], want[i])
		}
	}
}

// Изоляция включена, а bwrap нет — команда не выполняется вовсе, а не
// уходит без изоляции.
func TestIsolationWithoutBwrapRefuses(t *testing.T) {
	opts, root := hangTestOptions(t)
	opts.Isolation = Isolation{Kind: "bwrap"}
	t.Setenv("PATH", t.TempDir()) // bwrap не найдётся

	marker := filepath.Join(root, "ran.txt")
	_, err := runCommand(context.Background(), "echo x > "+marker, opts, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "bubblewrap") {
		t.Fatalf("ожидался отказ с объяснением, получено %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("команда выполнилась без изоляции")
	}
}

func TestBwrapFailedDetection(t *testing.T) {
	if !bwrapFailed(1, "bwrap: setting up uid map: Permission denied\n") {
		t.Error("отказ bwrap не распознан")
	}
	if bwrapFailed(1, "обычная ошибка команды") || bwrapFailed(2, "bwrap: x") {
		t.Error("ошибка команды принята за отказ bwrap")
	}
}

// Живая проверка: идёт только там, где bwrap есть и работает.
func TestBwrapIsolationLive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap есть только в Linux")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap не установлен")
	}
	if out, err := exec.Command("bwrap", "--unshare-all", "--ro-bind", "/", "/", "true").CombinedOutput(); err != nil {
		t.Skipf("bwrap здесь не работает: %v %s", err, out)
	}
	// Каталоги — вне /tmp: внутри изоляции /tmp свой, и проверка «снаружи
	// писать нельзя» в нём ничего бы не доказала.
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("нет каталога для проверки: %v", err)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Skipf("нет каталога для проверки: %v", err)
	}
	base, err := os.MkdirTemp(cache, "ollchat-bwrap-test-")
	if err != nil {
		t.Skipf("нет каталога для проверки: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, _ = filepath.EvalSymlinks(base)
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	hidden := filepath.Join(base, "hidden")
	for _, d := range []string{root, outside, hidden} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(hidden, "secret.txt"), []byte("тайна"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb, err := permissions.NewSandbox(root, false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Sandbox: sb, MaxOutputKB: 64,
		Isolation: Isolation{Kind: "bwrap", Network: false, Hide: []string{hidden}}}
	run := func(cmd string, timeout time.Duration) (string, error) {
		t.Helper()
		return runCommand(context.Background(), cmd, opts, timeout)
	}

	out, err := run("echo внутри > in.txt && cat in.txt", 10*time.Second)
	if err != nil || !strings.Contains(out, "внутри") {
		t.Fatalf("запись в корне: %v\n%s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(root, "in.txt")); err != nil || !strings.Contains(string(data), "внутри") {
		t.Fatalf("файл в корне не появился: %v", err)
	}

	out, err = run("echo снаружи > "+filepath.Join(outside, "out.txt"), 10*time.Second)
	t.Logf("запись вне корня:\n%s", out)
	if err != nil || strings.Contains(out, "Код возврата: 0") {
		t.Errorf("запись вне корня прошла: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(outside, "out.txt")); err == nil {
		t.Error("команда записала файл вне корня песочницы")
	}

	out, _ = run("cat "+filepath.Join(hidden, "secret.txt"), 10*time.Second)
	t.Logf("чтение спрятанного:\n%s", out)
	if strings.Contains(out, "тайна") {
		t.Errorf("спрятанный файл прочитан:\n%s", out)
	}

	out, _ = run("cat /proc/net/dev", 10*time.Second)
	t.Logf("сеть внутри:\n%s", out)
	for _, line := range strings.Split(out, "\n") {
		name, _, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && !strings.Contains(name, " ") && name != "lo" && !strings.HasPrefix(name, "Код") {
			t.Errorf("без сети видно устройство %q", name)
		}
	}

	out, err = run("sh -c 'exit 3'", 10*time.Second)
	if err != nil || !strings.Contains(out, "Код возврата: 3") {
		t.Errorf("код возврата не дошёл: %v\n%s", err, out)
	}

	start := time.Now()
	_, err = run("sleep 30", time.Second)
	if err == nil || !strings.Contains(err.Error(), "таймауту") {
		t.Errorf("таймаут не сработал: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("снятие по таймауту заняло %s", elapsed.Round(time.Millisecond))
	}
}
