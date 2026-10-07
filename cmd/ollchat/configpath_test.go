package main

import (
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
