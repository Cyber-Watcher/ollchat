package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loadText — конфиг из строки через тот же Load, что у запуска; сервер
// Ollama дописывается, без него конфиг не читается вовсе.
func loadText(t *testing.T, text string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	text += "\n[[servers]]\nname = \"local\"\nurl = \"http://ollama.example:11434\"\nmodel = \"m\"\n"
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(p)
	return cfg, err
}

// Раздел [mcp.servers.<имя>] (этап 121, Б2): поля, умолчания, раскрытие ~.
func TestMCPServersParsed(t *testing.T) {
	cfg, err := loadText(t, `
[mcp.servers.wiki]
url        = "http://mcp.example:8377/mcp"
token_file = "~/wiki.token"
timeout    = "15s"
tools      = ["search", "page.read"]
allow      = ["search"]

[mcp.servers.off-one]
url     = "https://mcp.example/mcp"
enabled = false
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.MCP.Names(); strings.Join(got, ",") != "off-one,wiki" {
		t.Fatalf("имена серверов: %v", got)
	}
	w := cfg.MCP.Servers["wiki"]
	if !w.On() || w.TimeoutDuration() != 15*time.Second || len(w.Tools) != 2 || len(w.Allow) != 1 {
		t.Errorf("wiki: %+v", w)
	}
	if strings.HasPrefix(w.TokenFile, "~") {
		t.Errorf("token_file не раскрыт: %q", w.TokenFile)
	}
	off := cfg.MCP.Servers["off-one"]
	if off.On() || off.TimeoutDuration() != 60*time.Second {
		t.Errorf("off-one: включён %v, срок %v", off.On(), off.TimeoutDuration())
	}
	if !cfg.Has("mcp") {
		t.Error("раздел mcp написан, а Has его не видит — /mcp не попадёт в меню")
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("лишние предупреждения: %q", cfg.Warnings)
	}
}

// Ошибки — при запуске, с названием ключа.
func TestMCPServersRejected(t *testing.T) {
	cases := map[string]string{
		"подчёркивание в имени": "[mcp.servers.my_wiki]\nurl = \"http://mcp.example/mcp\"\n",
		"двойное подчёркивание": "[mcp.servers.a__b]\nurl = \"http://mcp.example/mcp\"\n",
		"заглавные":             "[mcp.servers.Wiki]\nurl = \"http://mcp.example/mcp\"\n",
		"без схемы":             "[mcp.servers.wiki]\nurl = \"mcp.example:8377/mcp\"\n",
		"чужая схема":           "[mcp.servers.wiki]\nurl = \"ws://mcp.example/mcp\"\n",
		"без адреса":            "[mcp.servers.wiki]\ntools = [\"x\"]\n",
		"неверный срок":         "[mcp.servers.wiki]\nurl = \"http://mcp.example/mcp\"\ntimeout = \"скоро\"\n",
		"allow не в tools":      "[mcp.servers.wiki]\nurl = \"http://mcp.example/mcp\"\ntools = [\"a\"]\nallow = [\"b\"]\n",
		"allow шаблоном":        "[mcp.servers.wiki]\nurl = \"http://mcp.example/mcp\"\nallow = [\"*\"]\n",
		"tools со скобкой":      "[mcp.servers.wiki]\nurl = \"http://mcp.example/mcp\"\ntools = [\"a(b)\"]\n",
	}
	for name, text := range cases {
		if _, err := loadText(t, text); err == nil {
			t.Errorf("%s: конфиг принят", name)
		} else if !strings.Contains(err.Error(), "mcp.servers.") {
			t.Errorf("%s: ошибка не называет раздел: %v", name, err)
		}
	}
}

// Неизвестный ключ — предупреждение, как у соседних разделов (решение
// владельца в unknownKeys: ключ, убранный из программы, запуск не роняет).
func TestMCPUnknownKeyWarned(t *testing.T) {
	cfg, err := loadText(t, "[mcp.servers.wiki]\nurl = \"http://mcp.example/mcp\"\nalow = [\"x\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(cfg.Warnings, "\n"), "mcp.servers.wiki.alow") {
		t.Errorf("опечатка alow не названа: %q", cfg.Warnings)
	}
}

// Пример из образца (--init-config), раскомментированный, читается без ошибок
// и предупреждений: образец не обещает ключей, которых нет.
func TestTemplateMCPExampleLoads(t *testing.T) {
	var lines []string
	in := false
	for _, l := range strings.Split(Template, "\n") {
		if strings.HasPrefix(l, "# [mcp.servers.") {
			in = true
		}
		if !in {
			continue
		}
		if !strings.HasPrefix(l, "# ") {
			break
		}
		lines = append(lines, strings.TrimPrefix(l, "# "))
	}
	if len(lines) < 5 {
		t.Fatalf("в образце нет примера [mcp.servers.*]: %q", lines)
	}
	cfg, err := loadText(t, strings.Join(lines, "\n")+"\n")
	if err != nil {
		t.Fatalf("пример из образца не читается: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("в примере неизвестные ключи: %q", cfg.Warnings)
	}
	if len(cfg.MCP.Servers) != 1 {
		t.Errorf("серверов в примере: %d", len(cfg.MCP.Servers))
	}
	for _, s := range cfg.MCP.Servers {
		if !strings.Contains(s.URL, "mcp.example") {
			t.Errorf("адрес примера не выдуманный: %q", s.URL)
		}
	}
}
