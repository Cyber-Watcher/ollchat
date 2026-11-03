package maint

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
)

// showServer изображает Ollama для сверки модели: версия, список моделей
// и /api/show с файлом весов в своём каталоге моделей.
func showServer(t *testing.T, version, digest, dir, weights string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/version"):
			_ = json.NewEncoder(w).Encode(map[string]any{"version": version})
		case strings.HasSuffix(r.URL.Path, "/api/tags"):
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{
				"name": "проба:latest", "digest": digest,
				"details": map[string]any{"quantization_level": "Q4_K_M"}}}})
		case strings.HasSuffix(r.URL.Path, "/api/show"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"modelfile":  "FROM " + dir + "/" + weights + "\nRENDERER qwen3.8\nPARSER qwen3.5\n",
				"parameters": "temperature 1", "template": "{{ .Prompt }}"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --nodes делает ту же сверку модели, что сборка перед стартом, и говорит
// о ней до запуска: одни веса при разных манифестах и версиях Ollama — пройдёт
// с предупреждением, разные веса — сборка откажет.
func TestNodesChecksModelOnNodes(t *testing.T) {
	a := showServer(t, "0.32.13", "22130167c4c20e20", "/usr/share/ollama/.ollama/models/blobs", "sha256-f5f1dd8920d4aaaa")
	b := showServer(t, "0.40.11", "2e5064f01462425a", "/srv/models/blobs", "sha256-f5f1dd8920d4aaaa")
	c := showServer(t, "0.40.11", "2e5064f01462425a", "/srv/models/blobs", "sha256-0000111122223333")

	run := func(urls ...string) string {
		cfg := &config.Config{}
		cfg.Graph.Model = "проба:latest"
		for i, u := range urls {
			cfg.Graph.Nodes = append(cfg.Graph.Nodes, config.GraphNode{Name: string(rune('a' + i)), URL: u, Workers: 1})
		}
		var out bytes.Buffer
		if err := Nodes(&out, cfg); err != nil {
			t.Fatalf("Nodes: %v", err)
		}
		return out.String()
	}

	got := run(a.URL, b.URL)
	if !strings.Contains(got, "совпадают") {
		t.Errorf("одни веса с разным манифестом не приняты:\n%s", got)
	}
	if !strings.Contains(got, "внимание: версии Ollama") || !strings.Contains(got, "0.40.11") {
		t.Errorf("нет предупреждения о разных версиях Ollama:\n%s", got)
	}

	got = run(a.URL, c.URL)
	if !strings.Contains(got, "сборка ОТКАЖЕТ") || !strings.Contains(got, "разные веса") {
		t.Errorf("разные веса не названы отказом:\n%s", got)
	}
}
