package maint

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
)

// Сборка графа на сервере, выбранном [graph].server, идёт с заголовками
// этого сервера — теми же, с которыми его проверяет /graph-server. Прежде
// сборка слала пустые: сервер за прокси проходил проверку, а ночная сборка
// на нём падала (аудит этапа 123). Оба пути — пул узлов (node_wait > 0)
// и один сервер (node_wait = 0).
func TestBuildSendsGraphServerHeaders(t *testing.T) {
	for _, wait := range []string{"0s", "1m"} {
		t.Run("node_wait="+wait, func(t *testing.T) {
			var mu sync.Mutex
			var seen []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.Header.Get("Authorization"))
				mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer tuf" {
					http.Error(w, "нет ключа", http.StatusUnauthorized)
					return
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/api/version"):
					_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.40.11"})
				case strings.HasSuffix(r.URL.Path, "/api/tags"):
					_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{
						"name": "проба:latest", "model": "проба:latest", "digest": "sha256:aaa",
						"details": map[string]any{"quantization_level": "Q4_K_M"}}}})
				case strings.HasSuffix(r.URL.Path, "/api/show"):
					_ = json.NewEncoder(w).Encode(map[string]any{
						"modelfile": "FROM /models/blobs/sha256-f1\n", "parameters": "temperature 1",
						"template": "{{ .Prompt }}"})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			root := t.TempDir()
			kbDir := filepath.Join(root, "kb")
			base, err := kb.OpenBase(kbDir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := base.Create("proba", ""); err != nil {
				t.Fatal(err)
			}
			base.Close()
			body := "[kb]\ndir = \"" + kbDir + "\"\n[log]\nenabled = false\n" +
				"[[servers]]\nname = \"local\"\nurl = \"http://ollama.example:11434\"\n" +
				"[[servers]]\nname = \"tuf\"\nurl = \"" + srv.URL + "\"\n" +
				"[servers.headers]\nAuthorization = \"Bearer tuf\"\n" +
				"[graph]\nmodel = \"проба:latest\"\nserver = \"tuf\"\nnode_wait = \"" + wait + "\"\n"
			p := filepath.Join(root, "config.toml")
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, _, err := config.Load(p)
			if err != nil {
				t.Fatal(err)
			}
			// Кусков нет — сборка откажет уже после проверки сервера; важно,
			// с чем она к нему пришла.
			_ = Build(io.Discard, cfg, "proba", BuildRun{LogPath: filepath.Join(root, "build.log")})
			mu.Lock()
			defer mu.Unlock()
			if len(seen) == 0 {
				t.Fatal("сборка не обратилась к серверу графа")
			}
			for i, h := range seen {
				if h != "Bearer tuf" {
					t.Errorf("запрос %d к серверу графа с Authorization = %q", i+1, h)
				}
			}
		})
	}
}
