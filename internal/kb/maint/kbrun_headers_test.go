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

// --kb-embed считает векторы на сервере векторов (kb.embed_server) с его
// заголовками — как поиск и служба. Прежде заголовков не было вовсе, и
// сервер за прокси отказывал счёту векторов (аудит этапа 123).
func TestEmbedRunSendsEmbedServerHeaders(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer vec" {
			http.Error(w, "нет ключа", http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/ps"):
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
		case strings.HasSuffix(r.URL.Path, "/api/tags"):
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{
				"name": "bge-m3:latest", "model": "bge-m3:latest", "digest": "sha256:e1"}}})
		case strings.HasSuffix(r.URL.Path, "/api/embed"):
			_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2,0.3]]}`))
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
	body := "[kb]\ndir = \"" + kbDir + "\"\nembed_model = \"bge-m3\"\nembed_server = \"vec\"\n[log]\nenabled = false\n" +
		"[[servers]]\nname = \"local\"\nurl = \"http://ollama.example:11434\"\n" +
		"[[servers]]\nname = \"vec\"\nurl = \"" + srv.URL + "\"\n" +
		"[servers.headers]\nAuthorization = \"Bearer vec\"\n"
	p := filepath.Join(root, "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	_ = Embed(io.Discard, cfg, "proba", EmbedRun{})
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("счёт векторов не обратился к серверу векторов")
	}
	for i, h := range seen {
		if h != "Bearer vec" {
			t.Errorf("запрос %d к серверу векторов с Authorization = %q", i+1, h)
		}
	}
}
