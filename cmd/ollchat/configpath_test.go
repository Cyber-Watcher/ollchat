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
