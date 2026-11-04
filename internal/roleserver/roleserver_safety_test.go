package roleserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// Проверка сервера после аудита этапа 123: сверяются все графы, которые
// уедут с записью [graph].server; прежний сервер — со своими заголовками;
// веса эмбеддера — того тега, которым уйдут вопросы; чужой текст и пароль
// из адреса в отказ не попадают.

// showOllama — Ollama со сверкой модели извлечения: версия, список моделей
// (имя → файл весов) и /api/show с этим файлом. auth — какой Authorization
// сервер требует; пусто — никакого.
func showOllama(t *testing.T, models map[string]string, auth string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth != "" && r.Header.Get("Authorization") != auth {
			http.Error(w, "нет ключа", http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/version"):
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.40.11"})
		case strings.HasSuffix(r.URL.Path, "/api/tags"):
			list := []map[string]any{}
			for name, weights := range models {
				list = append(list, map[string]any{"name": name, "model": name, "digest": "sha256:" + weights,
					"details": map[string]any{"quantization_level": "Q4_K_M"}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": list})
		case strings.HasSuffix(r.URL.Path, "/api/show"):
			var req struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"modelfile":  "FROM /models/blobs/sha256-" + models[req.Model] + "\n",
				"parameters": "temperature 1", "template": "{{ .Prompt }}"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func loadBody(t *testing.T, body string) (*config.Config, string) {
	t.Helper()
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

func twoServers(labURL, tufURL, extra string) string {
	return `
[general]
mode = "safe"
default_server = "lab"

[[servers]]
name = "lab"
url = "` + labURL + `"
model = "base-m:latest"

[[servers]]
name = "tuf"
url = "` + tufURL + `"
model = "base-m:latest"
` + extra
}

// /graph-server пишет общий [graph].server: сверяется модель рабочего графа,
// а не выбранного в сеансе, и каждого подраздела, который уедет вместе с
// ним. Подраздел со своим адресом не уезжает — и не сверяется.
func TestGraphCheckCoversEveryMovedGraph(t *testing.T) {
	lab := showOllama(t, map[string]string{"base-m:latest": "f1", "exp-m:latest": "f2", "own-m:latest": "f3"}, "")

	// Выбран опытный граф, а на tuf нет модели рабочего.
	tuf := showOllama(t, map[string]string{"exp-m:latest": "f2"}, "")
	cfg, _ := loadBody(t, twoServers(lab.URL, tuf.URL, `
[graph]
model = "base-m:latest"
url = "`+lab.URL+`"

[graph.exp]
model = "exp-m:latest"
`))
	if err := cfg.UseGraph("exp"); err != nil {
		t.Fatal(err)
	}
	r := run(t, cfg, Graph, "tuf", nil)
	if r.Err == nil || !strings.Contains(r.Err.Error(), "base-m:latest") || !strings.Contains(r.Err.Error(), "[graph]") {
		t.Errorf("рабочий граф не сверен (выбран exp): %v", r.Err)
	}

	// Модели рабочего графа на tuf хватает, а у подраздела, идущего за
	// [graph].server, своей модели там нет. Подраздел со своим url не в счёт.
	tuf = showOllama(t, map[string]string{"base-m:latest": "f1"}, "")
	cfg, _ = loadBody(t, twoServers(lab.URL, tuf.URL, `
[graph]
model = "base-m:latest"
url = "`+lab.URL+`"

[graph.exp]
model = "exp-m:latest"

[graph.own]
model = "own-m:latest"
url = "`+lab.URL+`"
`))
	r = run(t, cfg, Graph, "tuf", nil)
	if r.Err == nil || !strings.Contains(r.Err.Error(), "exp-m:latest") || !strings.Contains(r.Err.Error(), "[graph.exp]") {
		t.Errorf("подраздел, который уедет на tuf, не сверен: %v", r.Err)
	}
	if r.Err != nil && strings.Contains(r.Err.Error(), "own-m") {
		t.Errorf("сверен подраздел со своим адресом — он не уезжает: %v", r.Err)
	}
}

// Прежний сервер сборки сверяется со своими заголовками, а не с заголовками
// нового: иначе сервер за прокси «расходился» сам с собой. Заголовки
// прежнего неизвестны и он не ответил — замечание, не отказ.
func TestGraphCheckOldServerOwnHeaders(t *testing.T) {
	lab := showOllama(t, map[string]string{"base-m:latest": "f1"}, "Bearer lab")
	tuf := showOllama(t, map[string]string{"base-m:latest": "f1"}, "Bearer tuf")
	headers := func(s string) string {
		return strings.Replace(s, "model = \"base-m:latest\"\n", "model = \"base-m:latest\"\n[servers.headers]\nAuthorization = \"Bearer SRV\"\n", 2)
	}
	body := twoServers(lab.URL, tuf.URL, "\n[graph]\nmodel = \"base-m:latest\"\nserver = \"lab\"\n")
	body = strings.Replace(headers(body), "Bearer SRV", "Bearer lab", 1)
	body = strings.Replace(body, "Bearer SRV", "Bearer tuf", 1)
	cfg, _ := loadBody(t, body)
	r := run(t, cfg, Graph, "tuf", nil)
	if r.Err != nil {
		t.Fatalf("проверка отказала: %v", r.Err)
	}
	for _, n := range r.Notes {
		if strings.Contains(n, "прежн") {
			t.Errorf("прежний сервер с теми же весами назван расхождением: %q", n)
		}
	}

	// Прежний — адрес без имени сервера (заголовков у сборки нет), и без
	// заголовков он не отвечает: сверить не удалось — замечание.
	body = twoServers(lab.URL, tuf.URL, "\n[graph]\nmodel = \"base-m:latest\"\nurl = \""+lab.URL+"\"\n")
	body = strings.Replace(headers(body), "Bearer SRV", "Bearer lab", 1)
	body = strings.Replace(body, "Bearer SRV", "Bearer tuf", 1)
	cfg, _ = loadBody(t, body)
	r = run(t, cfg, Graph, "tuf", nil)
	if r.Err != nil {
		t.Fatalf("молчащий прежний сервер дал отказ: %v", r.Err)
	}
	if !strings.Contains(strings.Join(r.Notes, "\n"), "прежний сервер сборки сверить не удалось") {
		t.Errorf("о несверенном прежнем сервере не сказано: %q", r.Notes)
	}
}

// Веса эмбеддера — того тега, которым уйдут вопросы: bge-m3 значит
// bge-m3:latest, а не первый тег, начинающийся с «bge-m3:».
func TestEmbedCheckUsesExactTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/tags") {
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{
				{"name": "bge-m3:567m-fp16", "model": "bge-m3:567m-fp16", "digest": "sha256:e1"},
				{"name": "bge-m3:latest", "model": "bge-m3:latest", "digest": "sha256:e2"},
			}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	kbDir := t.TempDir()
	cfg, _ := loadConfig(t, srv.URL, srv.URL, "m", kbDir)
	dir := collection(t, kbDir, "books")
	meta := `{"magic":"OLLKBV1","model":"bge-m3","dim":4,"count":10,"digest":"sha256:e1"}`
	if err := os.WriteFile(filepath.Join(dir, "vectors.meta"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := run(t, cfg, Embed, "tuf", []string{dir}); r.Err == nil {
		t.Error("проверка прошла по весам bge-m3:567m-fp16, а вопросы уйдут к bge-m3:latest с другими")
	}
}

// Имена моделей чужого сервера с ESC/OSC/C1 в тексте отказа — видимыми
// знаками, а не управляющими: отказ печатается в ленту и в терминал.
func TestCheckErrorWithoutControlChars(t *testing.T) {
	lab := showOllama(t, map[string]string{"base-m:latest": "f1"}, "")
	evil := "x\x1b]0;PWNED\x07\x1b[2J\u009b31m"
	tuf := showOllama(t, map[string]string{evil: "f9"}, "")
	cfg, _ := loadBody(t, twoServers(lab.URL, tuf.URL, "\n[graph]\nmodel = \"base-m:latest\"\nurl = \""+lab.URL+"\"\n"))
	r := run(t, cfg, Graph, "tuf", nil)
	if r.Err == nil {
		t.Fatal("ожидался отказ: модели нет")
	}
	for _, c := range []string{"\x1b", "\x07", "\u009b"} {
		if strings.Contains(r.Err.Error(), c) {
			t.Errorf("управляющий знак %q в тексте отказа: %q", c, r.Err.Error())
		}
	}
}

// Пароль из адреса сервера в отказ не попадает, даже если сервер собран
// мимо загрузки конфига (она такие адреса не принимает).
func TestChatCheckRedactsAddress(t *testing.T) {
	r := checkChat(context.Background(), config.Server{Name: "x", URL: "http://user:s3cret@127.0.0.1:1"})
	if r.Err == nil {
		t.Fatal("ожидался отказ: сервер молчит")
	}
	if strings.Contains(r.Err.Error(), "s3cret") {
		t.Errorf("пароль в тексте отказа: %v", r.Err)
	}
}
