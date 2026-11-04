package roleserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/graph"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// fakeOllama изображает Ollama: версия и список моделей (имя → digest).
func fakeOllama(t *testing.T, models map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/version"):
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.40.11"})
		case strings.HasSuffix(r.URL.Path, "/api/tags"):
			list := []map[string]any{}
			for name, digest := range models {
				list = append(list, map[string]any{"name": name, "model": name, "digest": digest})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": list})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// loadConfig — конфиг из файла: lab (адрес labURL) — чат по умолчанию,
// tuf (адрес tufURL) — кандидат с моделью tufModel.
func loadConfig(t *testing.T, labURL, tufURL, tufModel, kbDir string) (*config.Config, string) {
	t.Helper()
	body := `
[general]
mode           = "safe"
default_server = "lab"   # сервер при старте

[kb]
dir         = "` + kbDir + `"
embed_model = "bge-m3"

[[servers]]
name  = "lab"
url   = "` + labURL + `"
model = "qwen3.8:latest"

[[servers]]
name  = "tuf"
url   = "` + tufURL + `"
model = "` + tufModel + `"
`
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, p
}

// collection — коллекция name в базе kbDir с паспортом, как у настоящей:
// без meta.json Base.Names её не видит.
func collection(t *testing.T, kbDir, name string) string {
	t.Helper()
	base, err := kb.OpenBase(kbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	dir := base.CollectionDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func run(t *testing.T, cfg *config.Config, kind Kind, name string, collDirs []string) Result {
	t.Helper()
	job, err := NewJob(cfg, kind, name, collDirs)
	if err != nil {
		t.Fatal(err)
	}
	return job.Run()
}

// Сервер чата: модель без тега находится как «:latest»; модели нет и сервер
// молчит — отказ с причиной.
func TestChatChecksModel(t *testing.T) {
	tuf := fakeOllama(t, map[string]string{"qwen3.8:latest": "sha256:aaa"})
	cfg, _ := loadConfig(t, "http://127.0.0.1:1", tuf.URL, "qwen3.8", t.TempDir())
	if r := run(t, cfg, Chat, "tuf", nil); r.Err != nil {
		t.Errorf("модель без тега не найдена как :latest: %v", r.Err)
	}

	cfg, _ = loadConfig(t, "http://127.0.0.1:1", tuf.URL, "qwen3.8:27b", t.TempDir())
	if r := run(t, cfg, Chat, "tuf", nil); r.Err == nil || !strings.Contains(r.Err.Error(), "qwen3.8:27b") {
		t.Errorf("модели нет — ждали отказ с её именем, получено %v", r.Err)
	}
	if r := run(t, cfg, Chat, "lab", nil); r.Err == nil || !strings.Contains(r.Err.Error(), "не ответил") {
		t.Errorf("молчащий сервер — ждали отказ «не ответил», получено %v", r.Err)
	}
}

// Под идущей сборкой графа сервер графа и векторов не выбирается, а чата —
// выбирается: сборка конфиг уже прочла, а докатка прочла бы новый.
func TestCheckIdleUnderBuild(t *testing.T) {
	kbDir := t.TempDir()
	dir := collection(t, kbDir, "books")
	if err := CheckIdle(Graph, "tuf", []string{dir}); err != nil {
		t.Fatalf("работы нет, а отказ: %v", err)
	}
	g, err := graph.Create(dir, "books", 10, graph.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	if err := g.Lock(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []Kind{Graph, Embed} {
		err := CheckIdle(kind, "tuf", []string{dir})
		if err == nil || !strings.Contains(err.Error(), "сборка графа") || !strings.Contains(err.Error(), "books") {
			t.Errorf("%s под сборкой: ждали отказ с «сборка графа» и именем коллекции, получено %v", kind.Command(), err)
		}
	}
	if err := CheckIdle(Chat, "tuf", []string{dir}); err != nil {
		t.Errorf("сервер чата под сборкой не выбран: %v", err)
	}
	if err := g.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := CheckIdle(Graph, "tuf", []string{dir}); err != nil {
		t.Errorf("сборка кончилась, а отказ остался: %v", err)
	}
}

// Эмбеддер сверяется с паспортом векторов КАЖДОЙ коллекции: kb.embed_server
// у всех один. Отказ называет только разошедшуюся.
func TestEmbedChecksEveryCollection(t *testing.T) {
	tuf := fakeOllama(t, map[string]string{"bge-m3:latest": "sha256:e1"})
	kbDir := t.TempDir()
	cfg, _ := loadConfig(t, "http://127.0.0.1:1", tuf.URL, "qwen3.8", kbDir)
	same, other := collection(t, kbDir, "books"), collection(t, kbDir, "projectdocs")
	meta := func(dir, digest string) {
		body := `{"magic":"OLLKBV1","model":"bge-m3","dim":4,"count":10,"digest":"` + digest + `"}`
		if err := os.WriteFile(filepath.Join(dir, "vectors.meta"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	meta(same, "sha256:e1")
	meta(other, "sha256:e2")

	r := run(t, cfg, Embed, "tuf", []string{same, other})
	if r.Err == nil || !strings.Contains(r.Err.Error(), "projectdocs") || strings.Contains(r.Err.Error(), "books") {
		t.Fatalf("ждали отказ только по projectdocs, получено %v", r.Err)
	}
	meta(other, "sha256:e1")
	if r := run(t, cfg, Embed, "tuf", []string{same, other}); r.Err != nil {
		t.Errorf("веса совпали у обеих коллекций, а отказ: %v", r.Err)
	}
}

// Сервер чата по умолчанию пишется на место прежнего значения: комментарий
// строки остаётся, в окне выбор применяется.
func TestWriteChatServer(t *testing.T) {
	cfg, p := loadConfig(t, "http://127.0.0.1:1", "http://127.0.0.1:2", "qwen3.8", t.TempDir())
	if err := Write(p, Chat, "tuf", "выбран из ollchat: --use-chat-server"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), `default_server = "tuf"   # сервер при старте`) {
		t.Fatalf("значение не заменено на месте:\n%s", data)
	}
	if err := Apply(cfg, Chat, "tuf"); err != nil || cfg.General.DefaultServer != "tuf" {
		t.Errorf("в конфиге не применено: %q, %v", cfg.General.DefaultServer, err)
	}
	if err := Apply(cfg, Chat, "нет-такого"); err == nil {
		t.Error("неизвестный сервер применён")
	}
}

// Имя сервера проверяется до сети: чужое имя — перечень настоящих.
func TestNewJobUnknownServer(t *testing.T) {
	cfg, _ := loadConfig(t, "http://127.0.0.1:1", "http://127.0.0.1:2", "qwen3.8", t.TempDir())
	if _, err := NewJob(cfg, Graph, "tfu", nil); err == nil || !strings.Contains(err.Error(), "есть: lab, tuf") {
		t.Errorf("ждали отказ с перечнем серверов, получено %v", err)
	}
}
