package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/mcp"
)

// Вход /api/v1 службы (kbserve.Opts.Emb) считает вектор вопроса на сервере
// векторов (kb.embed_server) с ЕГО заголовками, как и инструменты MCP:
// прежде туда уходили заголовки первого сервера, и сервер векторов за
// прокси отказывал — поиск клиентов ollchat молча шёл по словам (аудит
// этапа 123).
func TestServiceDataEmbedderSendsEmbedServerHeaders(t *testing.T) {
	got := make(chan string, 4)
	vec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("X-Proxy-Auth")
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2,0.3]]}`))
	}))
	defer vec.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "[kb]\ndir = \"" + filepath.Join(dir, "kb") + "\"\nembed_model = \"bge-m3\"\nembed_server = \"vec\"\n" +
		"[log]\nenabled = false\n" +
		"[[servers]]\nname = \"local\"\nurl = \"http://ollama.example:11434\"\n" +
		"[servers.headers]\nX-Proxy-Auth = \"ключ чата\"\n" +
		"[[servers]]\nname = \"vec\"\nurl = \"" + vec.URL + "\"\n" +
		"[servers.headers]\nX-Proxy-Auth = \"ключ векторов\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_, data, err := build(cfg, false, mcp.ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	q := fmt.Sprintf("заголовки входа данных %d", time.Now().UnixNano())
	if _, err := data.Emb.Embed(context.Background(), []string{q}); err != nil {
		t.Fatal(err)
	}
	if h := <-got; h != "ключ векторов" {
		t.Errorf("вход данных службы пришёл к серверу векторов с X-Proxy-Auth = %q", h)
	}
}
