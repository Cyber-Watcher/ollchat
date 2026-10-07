package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// По умолчанию изоляция выключена: команды работают как раньше, а прятать
// предлагается то, где лежат ключи и токены.
func TestIsolationOffByDefault(t *testing.T) {
	c := Default()
	if c.Sandbox.Isolation != "" || !c.Sandbox.IsolationNetwork {
		t.Fatalf("умолчание изменилось: isolation=%q, network=%v", c.Sandbox.Isolation, c.Sandbox.IsolationNetwork)
	}
	if err := c.finalize(); err != nil {
		t.Fatalf("умолчания должны проходить проверку: %v", err)
	}
	for _, p := range c.Sandbox.IsolationHide {
		if !filepath.IsAbs(p) {
			t.Errorf("путь %q не раскрыт", p)
		}
	}
	if len(c.Sandbox.IsolationHide) != 4 {
		t.Errorf("прятать по умолчанию: %q", c.Sandbox.IsolationHide)
	}
}

func TestIsolationSettingsParsed(t *testing.T) {
	saved := isolationGOOS
	t.Cleanup(func() { isolationGOOS = saved })
	isolationGOOS = "linux"

	cfg, err := writeConfig(t, `
[sandbox]
isolation = "bwrap"
isolation_network = false
isolation_hide = ["~/.секреты", "/srv/keys"]
`)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Sandbox
	if s.Isolation != IsolationBwrap || s.IsolationNetwork {
		t.Fatalf("настройки не дошли: %+v", s)
	}
	if len(s.IsolationHide) != 2 || !filepath.IsAbs(s.IsolationHide[0]) || s.IsolationHide[1] != "/srv/keys" {
		t.Fatalf("isolation_hide: %q", s.IsolationHide)
	}
}

// Опечатка в названии — ошибка запуска, а не тихая работа без изоляции.
func TestIsolationRejectsUnknownKind(t *testing.T) {
	_, err := writeConfig(t, "[sandbox]\nisolation = \"docker\"\n")
	if err == nil || !strings.Contains(err.Error(), "sandbox.isolation") {
		t.Fatalf("ожидалась ошибка с именем настройки, получено %v", err)
	}
}

// bubblewrap есть только в Linux: на macOS настройка — ошибка, а не обещание,
// которое молча не выполняется.
func TestIsolationLinuxOnly(t *testing.T) {
	saved := isolationGOOS
	t.Cleanup(func() { isolationGOOS = saved })

	isolationGOOS = "darwin"
	_, err := writeConfig(t, "[sandbox]\nisolation = \"bwrap\"\n")
	if err == nil || !strings.Contains(err.Error(), "Linux") {
		t.Fatalf("на macOS ожидалась ошибка, получено %v", err)
	}

	isolationGOOS = "linux"
	if _, err := writeConfig(t, "[sandbox]\nisolation = \"bwrap\"\n"); err != nil {
		t.Fatalf("в Linux настройка допустима: %v", err)
	}
}

func TestIsolationHideNeedsAbsolutePath(t *testing.T) {
	_, err := writeConfig(t, "[sandbox]\nisolation_hide = [\"относительный/путь\"]\n")
	if err == nil || !strings.Contains(err.Error(), "isolation_hide") {
		t.Fatalf("относительный путь должен отклоняться, получено %v", err)
	}
}
