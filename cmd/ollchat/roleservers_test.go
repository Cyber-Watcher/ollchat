package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/roleserver"
)

// --role-servers печатает то, что возьмёт сборка: сервер графа по имени
// подставлен вместе с его узлами, эмбеддер — на своём сервере.
func TestPrintRoleServers(t *testing.T) {
	body := `
[general]
default_server = "lab"

[[servers]]
name  = "lab"
url   = "http://ollama.example:11434"
model = "m"

[[servers]]
name  = "tuf"
url   = "http://tuf.example:11434"
model = "m"

[[servers.nodes]]
name    = "gpu0"
url     = "http://tuf.example:11434"
workers = 2
probe   = "http://tuf.example:11435"

[[servers.nodes]]
name = "gpu1"
url  = "http://tuf.example:11436"

[kb]
embed_model  = "bge-m3"
embed_server = "tuf"

[graph]
model   = "qwen3.8:latest"
server  = "tuf"
workers = 4
`
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := printRoleServers(&b, cfg); err != nil {
		t.Fatal(err)
	}
	want := "graph\ttuf\thttp://tuf.example:11434\tqwen3.8:latest\n" +
		"node\tgpu0\thttp://tuf.example:11434\t2\thttp://tuf.example:11435\n" +
		"node\tgpu1\thttp://tuf.example:11436\t4\t-\n" +
		"embed\ttuf\thttp://tuf.example:11434\tbge-m3\n"
	if b.String() != want {
		t.Errorf("вывод:\n%s\nждали:\n%s", b.String(), want)
	}

	// Без ссылок по имени: пустой [graph].url — запасной адрес, как у сборки.
	cfg, _, err = config.Load(writeTmp(t, `
[general]
default_server = "lab"

[[servers]]
name  = "lab"
url   = "http://ollama.example:11434"
model = "m"
`))
	if err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if err := printRoleServers(&b, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), "graph\t-\thttp://ollama.example:11434\t") ||
		!strings.Contains(b.String(), "embed\t-\thttp://ollama.example:11434\t") {
		t.Errorf("запасной адрес не напечатан:\n%s", b.String())
	}
}

// --use-chat-server и --use-graph-server — тот же путь, что у ключа: модели
// нет — отказ с подсказкой --use-force и файл не тронут, --use-force пишет;
// под идущей сборкой графа отказ, и --use-force его не снимает.
func TestUseRoleServer(t *testing.T) {
	tuf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/tags") {
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen3.8:latest","model":"qwen3.8:latest"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(tuf.Close)
	kbDir := t.TempDir()
	p := writeTmp(t, `
[general]
default_server = "lab"

[kb]
dir = "`+kbDir+`"

[[servers]]
name  = "lab"
url   = "http://127.0.0.1:1"
model = "m"

[[servers]]
name  = "tuf"
url   = "`+tuf.URL+`"
model = "qwen3.8:27b"

[graph]
model = "qwen3.8:latest"
`)
	cfg, _, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	var out, errw strings.Builder

	err = useRoleServer(&out, &errw, cfg, roleserver.Chat, "tuf", false)
	if err == nil || !strings.Contains(err.Error(), "--use-chat-server tuf --use-force") {
		t.Fatalf("модели нет — ждали отказ с подсказкой --use-force, получено %v", err)
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Fatal("конфиг изменён, хотя проверка не прошла")
	}
	if err := useRoleServer(&out, &errw, cfg, roleserver.Chat, "tuf", true); err != nil {
		t.Fatalf("--use-force не записал: %v", err)
	}
	if after, _ := os.ReadFile(p); !strings.Contains(string(after), `default_server = "tuf"`) ||
		!strings.Contains(out.String(), "записано: general.default_server") {
		t.Fatalf("выбор не записан или не подтверждён:\n%s\n%s", after, out.String())
	}

	// Идущая сборка графа: признак LOCK держит этот процесс.
	base, err := kb.OpenBase(kbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	coll := base.CollectionDir("books")
	if err := os.MkdirAll(coll, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coll, "meta.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := graph.Create(coll, "books", 10, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	if err := g.Lock(); err != nil {
		t.Fatal(err)
	}
	before, _ = os.ReadFile(p)
	err = useRoleServer(&out, &errw, cfg, roleserver.Graph, "tuf", true)
	if err == nil || !strings.Contains(err.Error(), "сборка графа") {
		t.Fatalf("под сборкой ждали отказ, получено %v", err)
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Fatal("под сборкой конфиг изменён, --use-force обошёл отказ")
	}
}

// --use-force без выбора сервера — отказ запуска, а не тихий пропуск;
// с ключом выбора — проходит.
func TestUseForceNeedsServerKey(t *testing.T) {
	if err := checkCommandLine(newFlags(t, "--use-force")); err == nil ||
		!strings.Contains(err.Error(), "--use-force действует только") {
		t.Errorf("--use-force без --use-*-server: ждали отказ, получено %v", err)
	}
	if err := checkCommandLine(newFlags(t, "--use-force", "--use-chat-server=tuf")); err != nil {
		t.Errorf("--use-force с --use-chat-server отклонён: %v", err)
	}
}

func writeTmp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
