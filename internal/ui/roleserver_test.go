package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/roleserver"
)

// Выбор сервера графа и векторов из TUI (этап 123): проверка до записи,
// запись в файл конфига, применение в окне.

// fakeOllama изображает Ollama: версия, список моделей (имя → digest) и
// /api/show с файлом весов weights. Пустой список — моделей нет.
func fakeOllama(t *testing.T, models map[string]string, weights string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/version"):
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.40.11"})
		case strings.HasSuffix(r.URL.Path, "/api/tags"):
			list := []map[string]any{}
			for name, digest := range models {
				list = append(list, map[string]any{"name": name, "digest": digest,
					"details": map[string]any{"quantization_level": "Q4_K_M"}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": list})
		case strings.HasSuffix(r.URL.Path, "/api/show"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"modelfile":  "FROM /models/blobs/" + weights + "\nRENDERER qwen3.8\nPARSER qwen3.5\n",
				"parameters": "temperature 1", "template": "{{ .Prompt }}"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// roleModel — модель TUI на конфиге из файла: сервер lab (адрес old) несёт
// чат и сборку, tuf (адрес new) — кандидат.
func roleModel(t *testing.T, oldURL, newURL string) (*Model, string) {
	t.Helper()
	body := `
[general]
mode           = "safe"
default_server = "lab"

[kb]
embed_model = "bge-m3"   # смысловой поиск

[[servers]]
name  = "lab"
url   = "` + oldURL + `"
model = "qwen3.8:latest"

[[servers]]
name  = "tuf"
url   = "` + newURL + `"
model = "qwen3.8:27b-q4_K_M"

[graph]
model   = "qwen3.8:latest"
url     = "` + oldURL + `"   # адрес сборки
workers = 2
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
	return m, p
}

func runRole(t *testing.T, m *Model, kind roleserver.Kind, arg string) {
	t.Helper()
	cmd := m.roleServerCmd(kind, arg)
	if cmd == nil {
		t.Fatalf("/%s %s: проверка не запущена; последняя строка: %q", kind.Command(), arg, m.blocks[len(m.blocks)-1].text)
	}
	m.Update(cmd())
}

func blocksText(m *Model) string {
	var out []string
	for _, b := range m.blocks {
		out = append(out, b.text)
	}
	return strings.Join(out, "\n")
}

// Сервер с той же моделью и теми же весами: выбор записан в [graph].server
// после url, применён в окне, о probe предупреждено (решение владельца).
func TestGraphServerChosen(t *testing.T) {
	model := map[string]string{"qwen3.8:latest": "sha256:aaa"}
	old := fakeOllama(t, model, "sha256-f5f1")
	nw := fakeOllama(t, map[string]string{"qwen3.8:latest": "sha256:bbb"}, "sha256-f5f1")
	m, p := roleModel(t, old.URL, nw.URL)

	runRole(t, m, roleGraph, "tuf")
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "# адрес сборки\nserver = \"tuf\"   # выбран из ollchat: /graph-server\nworkers = 2") {
		t.Fatalf("выбор не записан после url:\n%s", data)
	}
	if m.cfg.Graph.Server != "tuf" || m.cfg.Graph.URL != nw.URL {
		t.Errorf("в окне не применено: server %q, url %q", m.cfg.Graph.Server, m.cfg.Graph.URL)
	}
	text := blocksText(m)
	if !strings.Contains(text, "нет probe") || !strings.Contains(text, "[graph].server = \"tuf\"") {
		t.Errorf("нет предупреждения о probe или подтверждения:\n%s", text)
	}
	if strings.Contains(text, "сверка с прежним сервером") {
		t.Errorf("одни веса при разных манифестах названы расхождением:\n%s", text)
	}
	if marks := m.roleMarks(&m.cfg.Servers[1]); marks != "граф" {
		t.Errorf("метка сервера tuf в выборнике %q, ждали «граф»", marks)
	}
}

// Модели нет — выбор не пишется, сказано, как записать всё равно; force пишет.
func TestGraphServerWithoutModel(t *testing.T) {
	old := fakeOllama(t, map[string]string{"qwen3.8:latest": "sha256:aaa"}, "w")
	nw := fakeOllama(t, map[string]string{"qwen3.8:27b-q4_K_M": "sha256:ccc"}, "w")
	m, p := roleModel(t, old.URL, nw.URL)
	before, _ := os.ReadFile(p)

	runRole(t, m, roleGraph, "tuf")
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Fatal("конфиг изменён, хотя модели графа на сервере нет")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError || !strings.Contains(last.text, "/graph-server tuf force") {
		t.Errorf("отказ без подсказки force: %q", last.text)
	}
	if m.cfg.Graph.Server != "" {
		t.Errorf("в окне выбор применён: %q", m.cfg.Graph.Server)
	}

	runRole(t, m, roleGraph, "tuf force")
	if after, _ := os.ReadFile(p); !strings.Contains(string(after), `server = "tuf"`) {
		t.Error("force не записал выбор")
	}
}

// Другие веса у нового сервера — запись идёт, но расхождение с прежним названо:
// штамп графа хранит одно имя модели и смены весов не заметит.
func TestGraphServerOtherWeights(t *testing.T) {
	old := fakeOllama(t, map[string]string{"qwen3.8:latest": "sha256:aaa"}, "sha256-f5f1")
	nw := fakeOllama(t, map[string]string{"qwen3.8:latest": "sha256:bbb"}, "sha256-0000")
	m, _ := roleModel(t, old.URL, nw.URL)
	runRole(t, m, roleGraph, "tuf")
	if text := blocksText(m); !strings.Contains(text, "сверка с прежним сервером") {
		t.Errorf("разные веса с прежним сервером не названы:\n%s", text)
	}
}

// Векторы: веса bge на выбранном сервере сверяются с паспортом векторов
// коллекции. Другие — отказ; те же — запись в kb.embed_server.
func TestEmbedServerChecksVectors(t *testing.T) {
	old := fakeOllama(t, map[string]string{"bge-m3:latest": "sha256:e1"}, "w")
	other := fakeOllama(t, map[string]string{"bge-m3:latest": "sha256:e2"}, "w")
	m, p := roleModel(t, old.URL, other.URL)

	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := base.CollectionDir("books")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"magic":"OLLKBV1","model":"bge-m3","dim":4,"count":10,"digest":"sha256:e1"}`
	if err := os.WriteFile(filepath.Join(dir, "vectors.meta"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	// Паспорт коллекции: без него Base.Names её не видит, а выбор эмбеддера
	// сверяется со всеми коллекциями базы, а не с одной открытой.
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m.kb.base, m.kb.use = base, "books"

	runRole(t, m, roleEmbed, "tuf")
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError || !strings.Contains(last.text, "другим файлом модели") {
		t.Fatalf("другие веса bge не остановили выбор: %q", last.text)
	}
	if data, _ := os.ReadFile(p); strings.Contains(string(data), "embed_server") {
		t.Fatal("конфиг изменён несмотря на отказ")
	}

	runRole(t, m, roleEmbed, "lab")
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "embed_model = \"bge-m3\"   # смысловой поиск\nembed_server = \"lab\"") {
		t.Fatalf("kb.embed_server не записан после embed_model:\n%s", data)
	}
	if m.cfg.KB.EmbedServer != "lab" || m.cfg.KB.EmbedURL != old.URL {
		t.Errorf("в окне не применено: %q %q", m.cfg.KB.EmbedServer, m.cfg.KB.EmbedURL)
	}
}

// Без имени — кто несёт роли; лишнее слово — подсказка использования; чужое
// имя — перечень серверов. Ни одна из трёх не идёт в сеть.
func TestRoleServerArgs(t *testing.T) {
	m, _ := roleModel(t, "http://ollama.example:11434", "http://tuf.example:11434")
	if cmd := m.roleServerCmd(roleGraph, ""); cmd != nil {
		t.Error("без имени запущена проверка")
	}
	if text := m.blocks[len(m.blocks)-1].text; !strings.Contains(text, "сборка графа: адрес http://ollama.example:11434") {
		t.Errorf("кто несёт роли не сказано: %q", text)
	}
	if cmd := m.roleServerCmd(roleGraph, "tuf now"); cmd != nil || !strings.Contains(m.blocks[len(m.blocks)-1].text, "использование") {
		t.Error("лишнее слово принято")
	}
	if cmd := m.roleServerCmd(roleEmbed, "tfu"); cmd != nil || !strings.Contains(m.blocks[len(m.blocks)-1].text, "есть: lab, tuf") {
		t.Error("неизвестное имя без перечня серверов")
	}
}
