package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Выбор сервера из TUI после аудита этапа 123: поздний итог не ложится
// поверх более позднего выбора, Esc снимает проверку, под конфигом проекта
// проверяется сервер общего конфига, чужой текст — без управляющих знаков,
// эмбеддер пересобирается при смене заголовков.

var graphServerRe = regexp.MustCompile(`(?m)^server = "([a-z]+)"`)

func graphServerIn(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if m := graphServerRe.FindStringSubmatch(string(data)); len(m) == 2 {
		return m[1]
	}
	return ""
}

// Человек набрал /graph-server tuf force, передумал: Esc и /graph-server lab.
// Пока первая проверка идёт, вторая команда отказывает; Esc её снимает;
// итог снятой проверки, пришедший последним, не пишется поверх lab.
func TestRoleCheckLateResultIgnored(t *testing.T) {
	model := map[string]string{"qwen3.8:latest": "sha256:aaa"}
	lab := fakeOllama(t, model, "w")
	tuf := fakeOllama(t, map[string]string{"other:latest": "sha256:bbb"}, "w")
	m, p := roleModel(t, lab.URL, tuf.URL)

	cmdTuf := m.roleServerCmd(roleGraph, "tuf force")
	if cmdTuf == nil {
		t.Fatalf("проверка tuf не запущена: %s", blocksText(m))
	}
	if cmd := m.roleServerCmd(roleGraph, "lab"); cmd != nil {
		t.Error("вторая проверка запущена, пока идёт первая")
	} else if !strings.Contains(blocksText(m), "уже идёт") {
		t.Errorf("отказ второй команды не назван:\n%s", blocksText(m))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	cmdLab := m.roleServerCmd(roleGraph, "lab")
	if cmdLab == nil {
		t.Fatalf("после Esc проверка lab не запущена: %s", blocksText(m))
	}
	m.Update(cmdLab())
	if got := graphServerIn(t, p); got != "lab" {
		t.Fatalf("после проверки lab в конфиге %q", got)
	}
	m.Update(cmdTuf()) // итог снятой проверки приходит последним
	if got := graphServerIn(t, p); got != "lab" {
		t.Errorf("последней была команда lab, а в конфиге [graph].server = %q", got)
	}
	if m.cfg.Graph.Server != "lab" {
		t.Errorf("в окне Graph.Server = %q, ждали lab", m.cfg.Graph.Server)
	}
}

// Под конфигом проекта со своими [[servers]] проверяется сервер ОБЩЕГО
// конфига: туда пишется выбор, и по нему его разрешит сборка.
func TestRoleCheckUsesGlobalConfigUnderProject(t *testing.T) {
	model := map[string]string{"qwen3.8:latest": "sha256:aaa"}
	lab := fakeOllama(t, model, "w")
	tuf := fakeOllama(t, model, "w")
	m, p := roleModel(t, lab.URL, tuf.URL)
	proj := []byte("[[servers]]\nname = \"lab\"\nurl = \"" + lab.URL + "\"\nmodel = \"qwen3.8:latest\"\n\n" +
		"[[servers]]\nname = \"tuf\"\nurl = \"http://127.0.0.1:1\"\nmodel = \"qwen3.8:latest\"\n")
	if err := m.cfg.ApplyProject(filepath.Join(t.TempDir(), "config.toml"), proj); err != nil {
		t.Fatal(err)
	}
	runRole(t, m, roleGraph, "tuf")
	if got := graphServerIn(t, p); got != "tuf" {
		t.Errorf("сервер tuf общего конфига исправен, а выбор не записан (проверен tuf проекта?):\n%s", blocksText(m))
	}
	if text := blocksText(m); strings.Contains(text, "адрес http://127.0.0.1:1.") || !strings.Contains(text, "адрес "+tuf.URL) {
		t.Errorf("подтверждение называет не тот адрес, что возьмёт сборка (%s):\n%s", tuf.URL, text)
	}
}

// Имена моделей чужого сервера с OSC и CSI не доходят до отрисованной ленты.
func TestRoleCheckFeedWithoutControlChars(t *testing.T) {
	lab := fakeOllama(t, map[string]string{"qwen3.8:latest": "sha256:aaa"}, "w")
	evil := fakeOllama(t, map[string]string{"x\x1b]0;PWNED\x07\x1b[2J": "sha256:bbb"}, "w")
	m, _ := roleModel(t, lab.URL, evil.URL)
	runRole(t, m, roleGraph, "tuf")
	for _, r := range m.rendered {
		if strings.Contains(r, "\x1b]0;PWNED") || strings.Contains(r, "\x1b[2J") {
			t.Fatalf("OSC/CSI чужого сервера в ленте: %q", r)
		}
	}
	if !strings.Contains(blocksText(m), `\x1b]0;PWNED`) {
		t.Errorf("имя модели пропало из отказа, а не стало видимым:\n%s", blocksText(m))
	}
}

// Эмбеддер окна пересобирается не только при смене адреса, но и при смене
// заголовков: kb.embed_url задан, адрес тот же, а сервер чата сменили —
// ключ прежнего сервера не должен уходить дальше.
func TestEmbedderFollowsHeaders(t *testing.T) {
	got := make(chan string, 4)
	emb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2,0.3]]}`))
	}))
	t.Cleanup(emb.Close)
	body := `
[general]
mode = "safe"
default_server = "lab"

[kb]
embed_model = "bge-m3"
embed_url = "` + emb.URL + `"

[[servers]]
name = "lab"
url = "http://127.0.0.1:1"
model = "m:latest"
[servers.headers]
Authorization = "Bearer lab"

[[servers]]
name = "tuf"
url = "http://127.0.0.1:2"
model = "m:latest"
[servers.headers]
Authorization = "Bearer tuf"
`
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModelWith(t, func(c *config.Config) { *c = *loaded })
	ask := func(server int) string {
		m.server = &m.cfg.Servers[server]
		q := fmt.Sprintf("заголовки %d %d", server, time.Now().UnixNano())
		if _, err := m.embedder().Embed(context.Background(), []string{q}); err != nil {
			t.Fatal(err)
		}
		return <-got
	}
	if h := ask(0); h != "Bearer lab" {
		t.Fatalf("сервер чата lab: заголовок %q", h)
	}
	if h := ask(1); h != "Bearer tuf" {
		t.Errorf("сервер чата сменён на tuf, а эмбеддер ушёл с заголовком %q", h)
	}
}
