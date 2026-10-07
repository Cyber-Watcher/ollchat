package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyber-Watcher/ollchat/internal/config"
	"github.com/Cyber-Watcher/ollchat/internal/kb"
	"github.com/Cyber-Watcher/ollchat/internal/kbserve"
	"github.com/Cyber-Watcher/ollchat/internal/permissions"
	"github.com/Cyber-Watcher/ollchat/internal/tools"
)

// dialogRegistry — реестр, каким его собирает диалог по умолчанию: с пишущими
// инструментами рядом с инструментами графа.
func dialogRegistry(t *testing.T, root string) *tools.Registry {
	t.Helper()
	sb, err := permissions.NewSandbox(root, false, false, 1024)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry(
		[]string{tools.NameBash, tools.NameWriteFile, tools.NameGraphSearch, tools.NameGraphEntity},
		tools.Options{Sandbox: sb, BashTimeout: 5 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// Графовый вход службы не исполняет ничего, кроме инструментов графа.
//
// Это регрессия, а не пожелание: до правки `{"name":"bash",…}` на
// /api/v1/graph/tool выполнял команду от имени службы — реестр был диалоговым,
// а проверялось только «включён ли инструмент». Служба держит общий ключ
// (или не держит никакого), так что это было выполнение команд по сети.
func TestServeGraphToolRefusesWritingTools(t *testing.T) {
	root := t.TempDir()
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	full := dialogRegistry(t, root)

	// Обе преграды проверяются порознь: сначала служба с ПОЛНЫМ реестром —
	// одной сверки имени должно хватить, даже если урезание реестра однажды
	// сломают.
	for _, reg := range []*tools.Registry{full, mustGraphRegistry(t, full)} {
		svc := &graphService{cfg: config.Default(), registry: reg, base: base}
		mux := kbserve.Handler(kbserve.Opts{Base: base, Graph: svc, Token: "k"})

		marker := filepath.Join(root, "pwned")
		for _, req := range []kbserve.GraphToolRequest{
			{Name: tools.NameBash, Args: map[string]any{"command": "touch " + marker}},
			{Name: tools.NameWriteFile, Args: map[string]any{"path": marker, "content": "x"}},
		} {
			body, _ := json.Marshal(req)
			r := httptest.NewRequest(http.MethodPost, "/api/v1/graph/tool", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer k")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)

			if w.Code != http.StatusForbidden {
				t.Errorf("%s через графовый вход: код %d, ожидался 403; тело: %s",
					req.Name, w.Code, w.Body.String())
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatalf("%s через графовый вход исполнился: файл %s создан", req.Name, marker)
			}
		}
	}
}

// Урезанный реестр содержит только инструменты графа, и только включённые.
func TestGraphRegistryKeepsOnlyGraphTools(t *testing.T) {
	reg := mustGraphRegistry(t, dialogRegistry(t, t.TempDir()))
	for _, name := range []string{tools.NameBash, tools.NameWriteFile} {
		if reg.Has(name) {
			t.Errorf("%s попал в набор графового входа", name)
		}
	}
	for _, name := range []string{tools.NameGraphSearch, tools.NameGraphEntity} {
		if !reg.Has(name) {
			t.Errorf("%s выпал из набора графового входа", name)
		}
	}
	// Выключенный у администратора инструмент графа не появляется сам.
	if reg.Has(tools.NameGraphPath) {
		t.Errorf("%s не был включён, но попал в набор", tools.NameGraphPath)
	}
}

// Инструменты графа сверку имени проходят: отказ, если он есть, — уже по делу
// (здесь — нет коллекции), а не «служба этого не исполняет».
func TestServeGraphToolLetsGraphToolsThrough(t *testing.T) {
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	full := dialogRegistry(t, t.TempDir())
	svc := &graphService{cfg: config.Default(), registry: mustGraphRegistry(t, full), base: base}
	_, err = svc.Tool(context.Background(), "", tools.NameGraphSearch, map[string]any{"query": "мьютекс"})
	if errors.Is(err, kbserve.ErrToolNotServed) {
		t.Fatalf("инструмент графа отвергнут как неисполняемый: %v", err)
	}
}

// Без OLLMCP_TOKEN служба не открывает порт сети: отказ раньше всего прочего
// и с подсказкой, что делать. Петля без ключа — законный запуск: дальше
// служба спотыкается уже о пустую библиотеку, а не о ключ.
func TestServeRefusesNetworkWithoutToken(t *testing.T) {
	t.Setenv("OLLMCP_TOKEN", "")
	base, err := kb.OpenBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"0.0.0.0:0", ":0"} {
		err := runServe(config.Default(), addr, true, nil, base, nil)
		if err == nil || !strings.Contains(err.Error(), "OLLMCP_TOKEN") {
			t.Errorf("--serve %s без ключа: %v, ожидался отказ с OLLMCP_TOKEN", addr, err)
		}
	}
	err = runServe(config.Default(), "127.0.0.1:0", true, nil, base, nil)
	if err == nil || strings.Contains(err.Error(), "OLLMCP_TOKEN") {
		t.Errorf("--serve 127.0.0.1 без ключа отвергнут из-за ключа: %v", err)
	}
}

// ollchat --serve --mcp собирает службу MCP той же mcp.NewService, что ollmcp:
// предел из ollmcp.toml рядом с конфигом виден в описании параметра, а вызов
// ложится в журнал шагов. Прежде здесь стоял голый NewServer — тот же набор
// шёл без пределов, срока вызова, потолка ответа и журнала.
func TestServeMCPUsesServiceSettings(t *testing.T) {
	dir := t.TempDir()
	base, err := kb.OpenBase(filepath.Join(dir, "kb"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Create("books", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ollmcp.toml"),
		[]byte("[limits]\ngraph_overview_top_k = 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Path = filepath.Join(dir, "config.toml")
	cfg.Log.Enabled = true
	cfg.Log.Dir = filepath.Join(dir, "logs")

	sb, err := permissions.NewSandbox(dir, false, false, 1024)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry([]string{tools.NameGraphOverview},
		tools.Options{Sandbox: sb, KB: base, KBDir: base.Dir()})
	if err != nil {
		t.Fatal(err)
	}
	mux, _, closeSvc, err := serveMux(cfg, "k", true, reg, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer k")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var d map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatalf("ответ /mcp не разобрался: %v (%s)", err, w.Body.String())
		}
		return d
	}

	list := post(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tl, _ := list["result"].(map[string]any)["tools"].([]any)
	desc := ""
	for _, x := range tl {
		tool := x.(map[string]any)
		if tool["name"] == tools.NameGraphOverview {
			props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
			desc, _ = props["top_k"].(map[string]any)["description"].(string)
		}
	}
	if !strings.Contains(desc, "1..3") {
		t.Errorf("предел из ollmcp.toml не дошёл до службы: описание top_k %q", desc)
	}

	post(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"graph_overview","arguments":{}}}`)
	closeSvc()
	logs, _ := filepath.Glob(filepath.Join(cfg.Log.Dir, "steps-*.jsonl"))
	found := false
	for _, f := range logs {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), tools.NameGraphOverview) {
			found = true
		}
	}
	if !found {
		t.Errorf("вызов по /mcp не попал в журнал шагов (%v)", logs)
	}
}

// Вектор вопроса служба считает с заголовками сервера из конфига: Ollama за
// прокси с авторизацией без них отказывала, и служба молча искала по словам.
func TestServeEmbedderSendsServerHeaders(t *testing.T) {
	got := make(chan string, 4)
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		got <- r.Header.Get("X-Proxy-Auth")
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2,0.3]]}`))
	}))
	defer ollama.Close()

	dir := t.TempDir()
	base, err := kb.OpenBase(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Create("books", ""); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.KB.Dir, cfg.KB.EmbedModel = dir, "bge-m3"
	cfg.Servers = []config.Server{{Name: "gpu", URL: ollama.URL,
		Headers: map[string]string{"X-Proxy-Auth": "секрет"}}}
	mux, _, closeSvc, err := serveMux(cfg, "", false, nil, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSvc()

	// Вопрос уникален: одиночные векторы кэшируются на весь процесс.
	body := fmt.Sprintf(`{"collection":"books","query":"заголовки %d"}`, time.Now().UnixNano())
	r := httptest.NewRequest(http.MethodPost, "/api/v1/search", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	select {
	case h := <-got:
		if h != "секрет" {
			t.Errorf("эмбеддер службы пришёл без заголовков сервера: X-Proxy-Auth = %q", h)
		}
	default:
		t.Fatalf("вектор вопроса не запрашивался: код %d, %s", w.Code, w.Body.String())
	}
}

func mustGraphRegistry(t *testing.T, full *tools.Registry) *tools.Registry {
	t.Helper()
	reg, err := graphRegistry(full)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
