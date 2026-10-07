package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Ключ -c сильнее переменной OLLCHAT_CONFIG, переменная сильнее умолчания.
func TestConfigPathOrder(t *testing.T) {
	if got := configPath("/a/flag.toml", "/b/env.toml"); got != "/a/flag.toml" {
		t.Errorf("ключ должен побеждать переменную: %q", got)
	}
	if got := configPath("", "/b/env.toml"); got != "/b/env.toml" {
		t.Errorf("без ключа берётся переменная: %q", got)
	}
	if got := configPath("", ""); got != config.DefaultPath() {
		t.Errorf("без ключа и переменной — умолчание: %q", got)
	}
	if got := configPath("", "~/x.toml"); strings.HasPrefix(got, "~") {
		t.Errorf("тильда в переменной не раскрыта: %q", got)
	}
}

// Без HOME сессии ложатся в свой каталог пользователя, а не в общий
// /tmp/ollchat-sessions, который делили все пользователи машины.
func TestSessionDirWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	got := sessionDir()
	want := filepath.Join(os.TempDir(), fmt.Sprintf("ollchat-sessions-%d", os.Getuid()))
	if got != want {
		t.Errorf("без HOME и кеша: %s, ожидалось %s", got, want)
	}
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	if got := sessionDir(); got != filepath.Join(cache, "ollchat", "sessions") {
		t.Errorf("без HOME, с XDG_CACHE_HOME: %s", got)
	}
}

// Неизвестные ключи конфига печатаются при запуске с путём к файлу:
// иначе опечатка молча оставляла бы умолчание.
func TestConfigWarningsPrinted(t *testing.T) {
	cfg := config.Default()
	cfg.Path = "/home/i/.config/ollchat/config.toml"
	cfg.Warnings = []string{"неизвестный ключ permissions.deny_list не действует"}
	var buf strings.Builder
	warnConfig(&buf, cfg)
	if s := buf.String(); !strings.Contains(s, cfg.Path) || !strings.Contains(s, "permissions.deny_list") {
		t.Errorf("предупреждение напечатано не так: %q", s)
	}
}
