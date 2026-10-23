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

// Имя kb в [mcp.servers] при заданном kb.server_url занято службой библиотеки
// (аудит 23.10.2026, находка 9): раздел kb с другим адресом прятал службу от
// ключей --mcp-* и --kb-remote-check, а шаг «tools/list» подключал тогда ВСЕ
// сторонние серверы. Теперь это отказ при загрузке; тот же адрес (служба,
// описанная сама) и kb без kb.server_url — законны.
func TestLibraryNameReserved(t *testing.T) {
	const lib = "[kb]\nserver_url = \"http://mcp.example:8377\"\n"
	_, err := loadText(t, lib+"[mcp.servers.kb]\nurl = \"http://198.51.100.10:9/mcp\"\n")
	if err == nil || !strings.Contains(err.Error(), "mcp.servers.kb") ||
		!strings.Contains(err.Error(), "зарезервировано") {
		t.Errorf("чужой адрес под именем kb: %v", err)
	}
	// Без /mcp на конце — тоже другой вход: служба отвечает по MCP на <адрес>/mcp.
	if _, err := loadText(t, lib+"[mcp.servers.kb]\nurl = \"http://mcp.example:8377\"\n"); err == nil {
		t.Error("адрес службы без /mcp под именем kb принят")
	}
	for name, text := range map[string]string{
		"та же служба":            lib + "[mcp.servers.kb]\nurl = \"HTTP://MCP.example:8377/mcp/\"\ntools = [\"kb_status\"]\n",
		"без kb.server_url":       "[mcp.servers.kb]\nurl = \"http://198.51.100.10:9/mcp\"\n",
		"другое имя":              lib + "[mcp.servers.wiki]\nurl = \"http://198.51.100.10:9/mcp\"\n",
		"служба под своим именем": lib + "[mcp.servers.books]\nurl = \"http://mcp.example:8377/mcp\"\n",
	} {
		if _, err := loadText(t, text); err != nil {
			t.Errorf("%s: отказ %v", name, err)
		}
	}
}

// Срок службы библиотеки один (аудит 23.10.2026, находка 10): сервер kb хаб
// строит из kb.server_timeout (mcphub/hub.go, MCPServer{Timeout: …}), и пустое
// значение давало ему 60 с — умолчание сторонних серверов, а не 30 с службы.
// Отрицательное грузилось молча.
func TestLibraryTimeoutSingleDefault(t *testing.T) {
	for name, text := range map[string]string{
		"не задан": "[kb]\nserver_url = \"http://mcp.example:8377\"\n",
		"пусто":    "[kb]\nserver_url = \"http://mcp.example:8377\"\nserver_timeout = \"\"\n",
		"ноль":     "[kb]\nserver_url = \"http://mcp.example:8377\"\nserver_timeout = \"0s\"\n",
	} {
		cfg, err := loadText(t, text)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := cfg.KB.ServerTimeoutDuration()
		if want != 30*time.Second {
			t.Errorf("%s: kb.server_timeout = %s, обещано 30 с", name, want)
		}
		// Ровно так хаб заводит сервер kb из kb.server_timeout.
		if got := (MCPServer{Timeout: cfg.KB.ServerTimeout}).TimeoutDuration(); got != want {
			t.Errorf("%s: сервер kb ждёт %s, служба — %s", name, got, want)
		}
	}
	// Конфиг, собранный кодом без файла (тесты, --init-config), — то же.
	if got := (MCPServer{Timeout: Default().KB.ServerTimeout}).TimeoutDuration(); got != 30*time.Second {
		t.Errorf("Default(): сервер kb ждёт %s", got)
	}
	cfg, err := loadText(t, "[kb]\nserver_timeout = \"45s\"\n")
	if err != nil || (MCPServer{Timeout: cfg.KB.ServerTimeout}).TimeoutDuration() != 45*time.Second {
		t.Errorf("заданный срок: %v", err)
	}
	for _, text := range []string{
		"[kb]\nserver_timeout = \"-5s\"\n",
		"[mcp.servers.wiki]\nurl = \"http://mcp.example/mcp\"\ntimeout = \"-5s\"\n",
	} {
		if _, err := loadText(t, text); err == nil {
			t.Errorf("отрицательный срок принят: %q", text)
		}
	}
}

// Ключ в адресе не поддерживается (решение владельца 23.10.2026): user:pass@,
// строка запроса и якорь в url сервера и в kb.server_url — отказ запуска,
// и сам ключ в текст отказа не попадает. Раньше они принимались молча вопреки
// «ключа здесь нет», а адрес идёт в /mcp, журналы и тексты ошибок.
func TestServiceURLSecretsRejected(t *testing.T) {
	for _, tail := range []string{"", "/books"} {
		for _, u := range []string{
			"http://user:SECRET@mcp.example:8377" + tail,
			"http://SECRET@mcp.example:8377" + tail,
			"http://mcp.example:8377" + tail + "?api_key=SECRET",
			"http://mcp.example:8377" + tail + "?",
			"http://mcp.example:8377" + tail + "#SECRET",
		} {
			for _, text := range []string{
				"[mcp.servers.wiki]\nurl = \"" + u + "\"\n",
				"[kb]\nserver_url = \"" + u + "\"\n",
			} {
				_, err := loadText(t, text)
				if err == nil {
					t.Errorf("принято: %q", text)
					continue
				}
				if strings.Contains(err.Error(), "SECRET") {
					t.Errorf("ключ в тексте отказа: %v", err)
				}
			}
		}
	}
	if _, err := loadText(t, "[kb]\nserver_url = \"http://\"\n"); err == nil {
		t.Error("kb.server_url без машины принят")
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
