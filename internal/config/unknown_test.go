package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Опечатка в имени ключа — предупреждение, а не молчание: `deny_list`
// вместо deny не запрещал ничего, и узнать об этом было не из чего.
// Отказом опечатка не становится — так решил владелец: убранный из программы
// ключ не должен мешать запуску со старым конфигом.
func TestUnknownKeyWarns(t *testing.T) {
	cfg, err := writeConfig(t, "[permissions]\ndeny_list = [\"Bash(rm:*)\"]\n")
	if err != nil {
		t.Fatalf("опечатка остановила запуск: %v", err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "permissions.deny_list") {
		t.Errorf("предупреждения: %q", cfg.Warnings)
	}
}

// Неизвестный раздел называется один раз, а не каждым своим ключом.
func TestUnknownSectionWarnsOnce(t *testing.T) {
	cfg, err := writeConfig(t, "[permisions]\nallow = []\ndeny = []\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "permisions") {
		t.Errorf("предупреждения: %q", cfg.Warnings)
	}
}

// Подраздел графа сверяется со своими настройками: опечатка в нём видна,
// а верные ключи и сам подраздел — нет.
func TestNamedGraphKeysChecked(t *testing.T) {
	cfg, err := writeConfig(t, "[graph.lab]\nmodel = \"x\"\nmodle = \"y\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "graph.lab.modle") {
		t.Errorf("предупреждения: %q", cfg.Warnings)
	}
}

// Свободные словари принимают любые ключи по смыслу: options уходит в Ollama
// как есть, цвета подсветки названы токенами лексера.
func TestFreeFormTablesNotWarned(t *testing.T) {
	body := `[theme]
code_theme = "gruvbox"

[theme.tokens]
NameTag = "#83a598"

[[servers]]
name = "local"
url  = "http://127.0.0.1:11434"

[servers.headers]
X-Token = "abc"

[servers.options]
num_ctx = 4096
nested = { a = 1 }
`
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("лишние предупреждения: %q", cfg.Warnings)
	}
}

// Предупреждение подсказывает, что имелось в виду (аудит 23.10.2026):
// `enable = false` оставлял сервер включённым, `[[mcp.servers]]` давал ноль
// серверов, и из «неизвестный ключ» не было видно, как написать верно.
func TestUnknownKeyHints(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string // что обязано быть в единственном предупреждении
	}{
		{"опечатка ключа сервера", "[mcp.servers.wiki]\nurl = \"http://mcp.example/mcp\"\nenable = false\n",
			[]string{"mcp.servers.wiki.enable ", "имелось в виду mcp.servers.wiki.enabled?"}},
		{"опечатка раздела", "[mcp.server.wiki]\nurl = \"http://mcp.example/mcp\"\n",
			[]string{"mcp.server.wiki", "имелось в виду mcp.servers?"}},
		{"лишняя буква", "[kb]\ntop_kk = 3\n", []string{"kb.top_kk ", "имелось в виду kb.top_k?"}},
		{"подраздел графа", "[graph.lab]\nmodle = \"x\"\n", []string{"graph.lab.modle ", "имелось в виду graph.lab.model?"}},
		{"массив вместо разделов", "[[mcp.servers]]\nname = \"wiki\"\nurl = \"http://mcp.example/mcp\"\nallow = [\"echo\"]\n",
			[]string{"[mcp.servers.<имя>]", "[[mcp.servers]]", "mcp.servers.url"}},
	}
	for _, c := range cases {
		cfg, err := writeConfig(t, c.body)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(cfg.Warnings) != 1 {
			t.Errorf("%s: предупреждений %d, ждали одно: %q", c.name, len(cfg.Warnings), cfg.Warnings)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(cfg.Warnings[0], w) {
				t.Errorf("%s: в предупреждении нет %q: %s", c.name, w, cfg.Warnings[0])
			}
		}
	}
	// Далёкое от всех ключей — без подсказки, а не с первой попавшейся.
	cfg, err := writeConfig(t, "[permissions]\ndeny_list = [\"Bash(rm:*)\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || strings.Contains(cfg.Warnings[0], "имелось в виду") {
		t.Errorf("подсказка к далёкому ключу: %q", cfg.Warnings)
	}
}

// Образец из --init-config чист: предупреждение о нём с первого запуска
// приучило бы предупреждения не читать.
func TestTemplateHasNoUnknownKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(Template), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("в образце неизвестные ключи: %q", cfg.Warnings)
	}
}
