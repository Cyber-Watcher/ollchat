package ui

import (
	"strings"
	"testing"

	"github.com/itpro/ollchat/internal/config"
	"github.com/itpro/ollchat/internal/permissions"
	"github.com/itpro/ollchat/internal/tools"
)

// registryWithout собирает набор инструментов без перечисленных имён.
func registryWithout(t *testing.T, skip ...string) *tools.Registry {
	t.Helper()
	sb, err := permissions.NewSandbox(t.TempDir(), false, false, 512)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range tools.AllNames() {
		drop := false
		for _, s := range skip {
			if n == s {
				drop = true
			}
		}
		if !drop {
			names = append(names, n)
		}
	}
	r, err := tools.NewRegistry(names, tools.Options{Sandbox: sb})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestToolDriftHint — конфиг от прежней версии молчит о новых инструментах,
// и это молчание уже стоило одного ложного вызова bash. Подсказка обязана
// назвать выключенное, путь к файлу настроек и способ посмотреть подробности.
func TestToolDriftHint(t *testing.T) {
	cfg := config.Default()
	cfg.Path = "/home/user/.config/ollchat/config.toml"

	hint := toolDriftHint(cfg, registryWithout(t, tools.NameViewImage, tools.NameKBSearch))
	for _, want := range []string{tools.NameViewImage, tools.NameKBSearch, "agent.tools", cfg.Path, "/tools"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("в подсказке нет %q: %q", want, hint)
		}
	}
	// Включённые перечислять нельзя — иначе подсказка врёт.
	if strings.Contains(hint, tools.NameReadFile) {
		t.Fatalf("в списке выключенных оказался включённый инструмент: %q", hint)
	}
}

// TestToolDriftHintSilentWhenComplete — полный набор поводов для шума не даёт.
func TestToolDriftHintSilentWhenComplete(t *testing.T) {
	cfg := config.Default()
	if hint := toolDriftHint(cfg, registryWithout(t)); hint != "" {
		t.Fatalf("подсказка при полном наборе: %q", hint)
	}
}

// TestToolDriftHintSilentWhenAgentOff — при выключенном агенте инструменты
// не передаются серверу вовсе, и говорить не о чем.
func TestToolDriftHintSilentWhenAgentOff(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.Enabled = false
	if hint := toolDriftHint(cfg, registryWithout(t, tools.NameViewImage)); hint != "" {
		t.Fatalf("подсказка при выключенном агенте: %q", hint)
	}
}

// TestToolDriftHintRendersApart проверяет, что подсказка — свой вид блока
// со своим цветом, а не приглушённая заметка.
func TestToolDriftHintRendersApart(t *testing.T) {
	r := newRenderer(80, false, "dark")
	got := r.Render(block{kind: blockHint, text: "Выключены инструменты: view_image."}, false)
	if !strings.Contains(got, "view_image") {
		t.Fatalf("текст подсказки потерян: %q", got)
	}
	notice := r.Render(block{kind: blockNotice, text: "Выключены инструменты: view_image."}, false)
	if got == notice {
		t.Fatalf("подсказка выглядит как обычная заметка: %q", got)
	}
}
